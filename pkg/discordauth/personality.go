// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package discordauth

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"
)

const HeaderInstallationID = "x-installation-id"
const HeaderDiscordLocale = "x-discord-locale"
const HeaderDiscordTimezone = "x-discord-timezone"
const HeaderSuperProperties = "x-super-properties"
const HeaderContextProperties = "x-context-properties"
const HeaderFingerprint = "x-fingerprint"
const HeaderDebugOptions = "x-debug-options"

const DefaultDebugOptions = "bugReporterEnabled"

// Personality encapsulates some settings that clients are likely to want to
// customize. These values are sent in nearly every HTTP request to Discord.
type Personality struct {
	UserAgent       string
	Locale          string          // `x-discord-locale`
	TimeZone        string          // `x-discord-timezone`
	DebugOptions    string          // `x-debug-options`
	SuperProperties SuperProperties // `x-super-properties` (base64)

	ExtraHeaders map[string]string
}

func (p *Personality) Headers() (http.Header, error) {
	superProps, err := p.SuperProperties.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal super properties: %w", err)
	}

	header := make(http.Header)
	header.Set("User-Agent", p.UserAgent)
	header.Set(HeaderDiscordLocale, p.Locale)
	header.Set(HeaderDiscordTimezone, p.TimeZone)
	header.Set(HeaderSuperProperties, string(superProps))

	for k, v := range p.ExtraHeaders {
		header.Set(k, v)
	}

	return header, nil
}

// FIXME(skip): This is missing client_heartbeat_session_id... that's only
// relevant when you have a gateway connection, though (?)

type SuperProperties struct {
	OS                     string                    `json:"os"`
	Browser                string                    `json:"browser"`
	Device                 string                    `json:"device"`
	SystemLocale           string                    `json:"system_locale"`
	HasClientMods          bool                      `json:"has_client_mods"`
	BrowserUserAgent       string                    `json:"browser_user_agent"`
	BrowserVersion         string                    `json:"browser_version"`
	OSVersion              string                    `json:"os_version"`
	Referrer               string                    `json:"referrer"`
	ReferringDomain        string                    `json:"referring_domain"`
	ReferrerCurrent        string                    `json:"referrer_current"`
	ReferringDomainCurrent string                    `json:"referring_domain_current"`
	ReleaseChannel         string                    `json:"release_channel"`
	ClientBuildNumber      int                       `json:"client_build_number"`
	ClientEventSource      *string                   `json:"client_event_source"`
	ClientLaunchID         string                    `json:"client_launch_id"`
	LaunchSignature        discordgo.LaunchSignature `json:"launch_signature"`
	ClientAppState         string                    `json:"client_app_state"`
}

var _ encoding.TextMarshaler = (*SuperProperties)(nil)

func (sp *SuperProperties) MarshalText() ([]byte, error) {
	// TODO(skip): Little bit of weird looking indirection here so we don't
	// recurse infinitely. Should probably just remove this, then.
	type superProperties SuperProperties
	spJson, err := json.Marshal((*superProperties)(sp))
	if err != nil {
		return nil, err
	}

	// Avoid the string() call that EncodeToString incurs.
	encoding := base64.StdEncoding
	buf := make([]byte, encoding.EncodedLen(len(spJson)))
	encoding.Encode(buf, spJson)

	return buf, nil
}
