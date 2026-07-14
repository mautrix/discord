// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package meowcord

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const (
	droidOS              = "Windows"
	droidOSVersion       = "10"
	droidBrowser         = "Chrome"
	droidReferrer        = "https://discord.com/channels/@me"
	droidReferringDomain = "discord.com"
	droidReleaseChannel  = "stable"
	droidStatus          = "invisible"
	droidSystemLocale    = "en-US"
)

var (
	droidCapabilities      = 1734653
	droidClientBuildNumber = 497254
	droidGatewayURL        = ""
	mainPageLoaded         = false
)

var mainPageLoadLock sync.Mutex

const (
	DroidBrowserMajorVersion = "144"
	DroidBrowserVersion      = DroidBrowserMajorVersion + ".0.0.0"
	DroidBrowserUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + DroidBrowserVersion + " Safari/537.36"
)

// BaseProperties contains the data common to both the X-Super-Properties header value
// and the properties sent during IDENTIFY.
type BaseProperties struct {
	OS                     string          `json:"os"`
	Browser                string          `json:"browser"`
	Device                 string          `json:"device"`
	SystemLocale           string          `json:"system_locale"`
	HasClientMods          bool            `json:"has_client_mods"`
	BrowserUserAgent       string          `json:"browser_user_agent"`
	BrowserVersion         string          `json:"browser_version"`
	OSVersion              string          `json:"os_version"`
	Referrer               string          `json:"referrer"`
	ReferringDomain        string          `json:"referring_domain"`
	ReferrerCurrent        string          `json:"referrer_current"`
	ReferringDomainCurrent string          `json:"referring_domain_current"`
	ReleaseChannel         string          `json:"release_channel"`
	ClientBuildNumber      int             `json:"client_build_number"`
	ClientEventSource      *string         `json:"client_event_source"`
	ClientLaunchID         uuid.UUID       `json:"client_launch_id"`
	LaunchSignature        LaunchSignature `json:"launch_signature"`
	// ClientAppState is either "unfocused" or "focused".
	ClientAppState string `json:"client_app_state"`
}

// SuperProperties is sent as the X-Super-Properties header.
type SuperProperties struct {
	BaseProperties
	ClientHeartbeatSessionID uuid.UUID `json:"client_heartbeat_session_id"`
}

// UserIdentifyProperties is sent when IDENTIFYing to the gateway.
type UserIdentifyProperties struct {
	BaseProperties
	IsFastConnect         bool   `json:"is_fast_connect"`
	GatewayConnectReasons string `json:"gateway_connect_reasons"`
}

type ClientState struct {
	GuildVersions            struct{} `json:"guild_versions"`
	HighestLastMessageID     string   `json:"highest_last_message_id,omitempty"`
	ReadStateVersion         int      `json:"read_state_version,omitempty"`
	UserGuildSettingsVersion int      `json:"user_guild_settings_version,omitempty"`
	UserSettingsVersion      int      `json:"user_settings_version,omitempty"`
	PrivateChannelsVersion   string   `json:"private_channels_version,omitempty"`
	APICodeVersion           int      `json:"api_code_version,omitempty"`
}

func mustMarshalJSON(data interface{}) string {
	dat, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(dat)
}

func basedOn(base map[string]string, additional map[string]string) map[string]string {
	for k, v := range base {
		_, exists := additional[k]
		if !exists {
			additional[k] = v
		}
	}
	return additional
}

func (s *Session) UpdateVersion(version, capabilities int) {
	droidClientBuildNumber = version
	droidCapabilities = capabilities
	droidBaseProperties.ClientBuildNumber = version
	s.UpdateUserHeaders()
}

func (s *Session) UpdateUserHeaders() {
	baseProps := *droidBaseProperties
	baseProps.LaunchSignature = s.launchSignature
	baseProps.ClientLaunchID = s.launchID

	superProps := SuperProperties{
		BaseProperties:           baseProps,
		ClientHeartbeatSessionID: s.HeartbeatSession.ID,
	}

	superPropsHeader := "X-Super-Properties"
	encodedSuperProps := mustMarshalJSON(superProps)
	s.fetchHeaders = basedOn(DroidBaseHeaders, map[string]string{
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
		"X-Debug-Options":    "bugReporterEnabled",
		"X-Discord-Locale":   droidSystemLocale,
		"X-Discord-Timezone": "UTC",
		superPropsHeader:     encodedSuperProps,
	})
	s.downloadHeaders = basedOn(s.fetchHeaders, map[string]string{
		"Sec-Fetch-Mode": "no-cors",
		superPropsHeader: encodedSuperProps,
	})
	s.imageHeaders = basedOn(s.downloadHeaders, map[string]string{
		"Accept":         "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
		"Sec-Fetch-Dest": "image",
		superPropsHeader: encodedSuperProps,
	})

	identifyProps := UserIdentifyProperties{
		BaseProperties:        baseProps,
		IsFastConnect:         false,
		GatewayConnectReasons: "AppSkeleton",
	}
	s.Identify.Properties = identifyProps
}

func (s *Session) SetGatewayURL(url string) {
	s.gateway = url
	s.noClearGateway = true
}

var apiVersionRegex = regexp.MustCompile(`"?API_VERSION"?:\s?(\d+),`)
var gatewayURLRegex = regexp.MustCompile(`"?GATEWAY_ENDPOINT"?:\s?['"](.+?)['"],`)
var mainJSRegex = regexp.MustCompile(`src="(/assets/web.[a-f0-9]{12,32}.js)"`)
var buildNumberRegex = regexp.MustCompile(`(?:buildNumber|build_number):\s?['"]?(\d{6,})['"]?`)

func (s *Session) LoadMainPage(ctx context.Context) error {
	mainPageLoadLock.Lock()
	defer mainPageLoadLock.Unlock()
	if mainPageLoaded && droidGatewayURL != "" {
		s.SetGatewayURL(droidGatewayURL)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/channels/@me", nil)
	if err != nil {
		return fmt.Errorf("failed to prepare request: %w", err)
	}
	for name, value := range DroidBaseHeaders {
		req.Header.Add(name, value)
	}
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch main page: %w", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("failed to read main page: %w", err)
	}

	apiVersionMatch := apiVersionRegex.FindSubmatch(data)
	if apiVersionMatch == nil {
		return fmt.Errorf("failed to find API version")
	} else if string(apiVersionMatch[1]) != APIVersion {
		return fmt.Errorf("API version mismatch: expected %s, got %s", APIVersion, apiVersionMatch[1])
	}
	gatewayURLMatch := gatewayURLRegex.FindSubmatch(data)
	if gatewayURLMatch == nil {
		return fmt.Errorf("failed to find gateway URL")
	}
	droidGatewayURL = string(gatewayURLMatch[1])
	if !strings.HasSuffix(droidGatewayURL, "/") {
		droidGatewayURL += "/"
	}
	s.log(LogInformational, "Found gateway URL %s and confirmed API version", droidGatewayURL)
	s.SetGatewayURL(droidGatewayURL)
	mainJSMatch := mainJSRegex.FindSubmatch(data)
	if mainJSMatch == nil {
		return fmt.Errorf("failed to find main JS URL")
	}

	jsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com"+string(mainJSMatch[1]), nil)
	if err != nil {
		return fmt.Errorf("failed to prepare JS request: %w", err)
	}
	for name, value := range DroidBaseHeaders {
		req.Header.Add(name, value)
	}
	jsReq.Header.Set("Sec-Fetch-Dest", "script")
	jsReq.Header.Set("Sec-Fetch-Mode", "no-cors")
	jsReq.Header.Set("Sec-Fetch-Site", "same-origin")
	jsReq.Header.Set("Accept", "*/*")
	jsResp, err := s.Client.Do(jsReq)
	if err != nil {
		return fmt.Errorf("failed to fetch JS: %w", err)
	}
	jsData, err := io.ReadAll(jsResp.Body)
	_ = jsResp.Body.Close()
	if err != nil {
		return fmt.Errorf("failed to read JS: %w", err)
	}
	buildNumberMatch := buildNumberRegex.FindSubmatch(jsData)
	if buildNumberMatch == nil {
		return fmt.Errorf("failed to find build number")
	}
	buildNumberInt, err := strconv.Atoi(string(buildNumberMatch[1]))
	if err != nil {
		return fmt.Errorf("failed to parse build number %s: %w", buildNumberMatch[1], err)
	}
	s.log(LogInformational, "Found build number %d from JS file %s", buildNumberInt, string(mainJSMatch[1]))
	// TODO parse capabilities too?
	s.UpdateVersion(buildNumberInt, droidCapabilities)
	mainPageLoaded = true

	return nil
}

var (
	droidBaseProperties = &BaseProperties{
		OS:               droidOS,
		OSVersion:        droidOSVersion,
		Browser:          droidBrowser,
		BrowserVersion:   DroidBrowserVersion,
		BrowserUserAgent: DroidBrowserUserAgent,
		//Referrer: droidReferrer,
		//ReferringDomain: droidReferringDomain,
		ClientBuildNumber: droidClientBuildNumber,
		ReleaseChannel:    droidReleaseChannel,
		SystemLocale:      droidSystemLocale,
		ClientAppState:    "focused",
	}
	DroidBaseHeaders = map[string]string{
		"Sec-Ch-Ua":          fmt.Sprintf(`" Not A;Brand";v="99", "Chromium";v="%[1]s", "Google Chrome";v="%[1]s"`, DroidBrowserMajorVersion),
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"` + droidOS + `"`,

		"Accept":          "*/*",
		"Origin":          "https://discord.com",
		"Accept-Language": "en-US,en;q=0.9",
		"User-Agent":      DroidBrowserUserAgent,
	}
	DroidFetchHeaders    = basedOn(DroidBaseHeaders, map[string]string{})
	DroidDownloadHeaders = basedOn(DroidFetchHeaders, map[string]string{
		"Sec-Fetch-Mode": "no-cors",
	})

	DroidWSHeaders = map[string]string{
		"User-Agent":      DroidBrowserUserAgent,
		"Origin":          "https://discord.com",
		"Accept-Language": "en-US,en;q=0.9",
		"Pragma":          "no-cache",
		"Cache-Control":   "no-cache",
		"Accept-Encoding": "gzip, deflate, br",

		//"Sec-Fetch-Dest": "websocket",
		//"Sec-Fetch-Mode": "websocket",
		//"Sec-Fetch-Site": "cross-site",
	}
)

const (
	ThreadJoinLocationContextMenu     = "Context Menu"
	ThreadJoinLocationToolbarOverflow = "Toolbar Overflow"
	ThreadJoinLocationSidebarOverflow = "Sidebar Overflow"
)

const (
	ReactionLocationHoverBar     = "Message Hover Bar"
	ReactionLocationInlineButton = "Message Inline Button"
	ReactionLocationPicker       = "Message Reaction Picker"
	ReactionLocationContextMenu  = "Message Context Menu"
)

func (s *Session) MessageReactionAddUser(guildID, channelID, messageID, emojiID string, options ...RequestOption) error {
	if s.IsUser {
		options = append(
			options,
			WithChannelReferer(guildID, channelID),
			WithLocationParam(ReactionLocationPicker),
			WithQueryParam("type", "0"),
		)
	}
	return s.MessageReactionAdd(channelID, messageID, emojiID, options...)
}

func (s *Session) MessageReactionRemoveUser(guildID, channelID, messageID, emojiID, userID string, options ...RequestOption) error {
	if s.IsUser {
		options = append(
			options,
			WithChannelReferer(guildID, channelID),
			WithLocationParam(ReactionLocationInlineButton),
			WithQueryParam("burst", "false"),
		)
	}
	return s.MessageReactionRemove(channelID, messageID, emojiID, userID, options...)
}
