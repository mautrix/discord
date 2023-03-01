// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/skip2/go-qrcode"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
	"go.mau.fi/mautrix-discord/remoteauth"
)

type WrappedCommandEvent struct {
	*commands.Event
	Bridge *DiscordBridge
	User   *User
	Portal *Portal
}

func (br *DiscordBridge) RegisterCommands() {
	proc := br.CommandProcessor.(*commands.Processor)
	proc.AddHandlers(
		cmdLoginToken,
		cmdLoginQR,
		cmdLogout,
		cmdReconnect,
		cmdDisconnect,
		cmdSetRelay,
		cmdUnsetRelay,
		cmdGuilds,
		cmdRejoinSpace,
		cmdDeleteAllPortals,
		cmdExec,
		cmdCommands,
	)
}

func wrapCommand(handler func(*WrappedCommandEvent)) func(*commands.Event) {
	return func(ce *commands.Event) {
		user := ce.User.(*User)
		var portal *Portal
		if ce.Portal != nil {
			portal = ce.Portal.(*Portal)
		}
		br := ce.Bridge.Child.(*DiscordBridge)
		handler(&WrappedCommandEvent{ce, br, user, portal})
	}
}

var cmdLoginToken = &commands.FullHandler{
	Func: wrapCommand(fnLoginToken),
	Name: "login-token",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to your Discord account by extracting the access token manually.",
		Args:        "<user/bot/oauth> <_token_>",
	},
}

const discordTokenEpoch = 1293840000

func decodeToken(token string) (userID int64, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		err = fmt.Errorf("invalid number of parts in token")
		return
	}
	var userIDStr []byte
	userIDStr, err = base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		err = fmt.Errorf("invalid base64 in user ID part: %w", err)
		return
	}
	_, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		err = fmt.Errorf("invalid base64 in random part: %w", err)
		return
	}
	_, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		err = fmt.Errorf("invalid base64 in checksum part: %w", err)
		return
	}
	userID, err = strconv.ParseInt(string(userIDStr), 10, 64)
	if err != nil {
		err = fmt.Errorf("invalid number in decoded user ID part: %w", err)
		return
	}
	return
}

func fnLoginToken(ce *WrappedCommandEvent) {
	if len(ce.Args) != 2 {
		ce.Reply("**Usage**: `$cmdprefix login-token <user/bot/oauth> <token>`")
		return
	}
	ce.MarkRead()
	defer ce.Redact()
	if ce.User.IsLoggedIn() {
		ce.Reply("You're already logged in")
		return
	}
	token := ce.Args[1]
	userID, err := decodeToken(token)
	if err != nil {
		ce.Reply("Invalid token")
		return
	}
	switch strings.ToLower(ce.Args[0]) {
	case "user":
		// Token is used as-is
	case "bot":
		token = "Bot " + token
	case "oauth":
		token = "Bearer " + token
	default:
		ce.Reply("Token type must be `user`, `bot` or `oauth`")
		return
	}
	ce.Reply("Connecting to Discord as user ID %d", userID)
	if err = ce.User.Login(token); err != nil {
		ce.Reply("Error connecting to Discord: %v", err)
		return
	}
	ce.Reply("Successfully logged in as %s#%s", ce.User.Session.State.User.Username, ce.User.Session.State.User.Discriminator)
}

var cmdLoginQR = &commands.FullHandler{
	Func:    wrapCommand(fnLoginQR),
	Name:    "login-qr",
	Aliases: []string{"login"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to your Discord account by scanning a QR code.",
	},
}

func fnLoginQR(ce *WrappedCommandEvent) {
	if ce.User.IsLoggedIn() {
		ce.Reply("You're already logged in")
		return
	}

	client, err := remoteauth.New()
	if err != nil {
		ce.Reply("Failed to prepare login: %v", err)
		return
	}

	qrChan := make(chan string)
	doneChan := make(chan struct{})

	var qrCodeEvent id.EventID

	go func() {
		code := <-qrChan
		resp := sendQRCode(ce, code)
		qrCodeEvent = resp
	}()

	ctx := context.Background()

	if err = client.Dial(ctx, qrChan, doneChan); err != nil {
		close(qrChan)
		close(doneChan)
		ce.Reply("Error connecting to login websocket: %v", err)
		return
	}

	<-doneChan

	if qrCodeEvent != "" {
		_, _ = ce.MainIntent().RedactEvent(ce.RoomID, qrCodeEvent)
	}

	user, err := client.Result()
	if err != nil || len(user.Token) == 0 {
		if restErr := (&discordgo.RESTError{}); errors.As(err, &restErr) &&
			restErr.Response.StatusCode == http.StatusBadRequest &&
			bytes.Contains(restErr.ResponseBody, []byte("captcha-required")) {
			ce.Reply("Error logging in: %v\n\nCAPTCHAs are currently not supported - use token login instead", err)
		} else {
			ce.Reply("Error logging in: %v", err)
		}
		return
	} else if err = ce.User.Login(user.Token); err != nil {
		ce.Reply("Error connecting after login: %v", err)
		return
	}
	ce.User.Lock()
	ce.User.DiscordID = user.UserID
	ce.User.Update()
	ce.User.Unlock()
	ce.Reply("Successfully logged in as %s#%s", user.Username, user.Discriminator)
}

func sendQRCode(ce *WrappedCommandEvent, code string) id.EventID {
	url, ok := uploadQRCode(ce, code)
	if !ok {
		return ""
	}

	content := event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    code,
		URL:     url.CUString(),
	}

	resp, err := ce.Bot.SendMessageEvent(ce.RoomID, event.EventMessage, &content)
	if err != nil {
		ce.Log.Errorfln("Failed to send QR code: %v", err)
		return ""
	}

	return resp.EventID
}

func uploadQRCode(ce *WrappedCommandEvent, code string) (id.ContentURI, bool) {
	qrCode, err := qrcode.Encode(code, qrcode.Low, 256)
	if err != nil {
		ce.Log.Errorln("Failed to encode QR code:", err)
		ce.Reply("Failed to encode QR code: %v", err)
		return id.ContentURI{}, false
	}

	resp, err := ce.Bot.UploadBytes(qrCode, "image/png")
	if err != nil {
		ce.Log.Errorln("Failed to upload QR code:", err)
		ce.Reply("Failed to upload QR code: %v", err)
		return id.ContentURI{}, false
	}

	return resp.ContentURI, true
}

var cmdLogout = &commands.FullHandler{
	Func: wrapCommand(fnLogout),
	Name: "logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Forget the stored Discord auth token.",
	},
}

func fnLogout(ce *WrappedCommandEvent) {
	wasLoggedIn := ce.User.DiscordID != ""
	ce.User.Logout()
	if wasLoggedIn {
		ce.Reply("Logged out successfully.")
	} else {
		ce.Reply("You weren't logged in, but data was re-cleared just to be safe.")
	}
}

var cmdDisconnect = &commands.FullHandler{
	Func: wrapCommand(fnDisconnect),
	Name: "disconnect",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Disconnect from Discord (without logging out)",
	},
	RequiresLogin: true,
}

func fnDisconnect(ce *WrappedCommandEvent) {
	if !ce.User.Connected() {
		ce.Reply("You're already not connected")
	} else if err := ce.User.Disconnect(); err != nil {
		ce.Reply("Error while disconnecting: %v", err)
	} else {
		ce.Reply("Successfully disconnected")
	}
}

var cmdReconnect = &commands.FullHandler{
	Func:    wrapCommand(fnReconnect),
	Name:    "reconnect",
	Aliases: []string{"connect"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Reconnect to Discord after disconnecting",
	},
	RequiresLogin: true,
}

func fnReconnect(ce *WrappedCommandEvent) {
	if ce.User.Connected() {
		ce.Reply("You're already connected")
	} else if err := ce.User.Connect(); err != nil {
		ce.Reply("Error while reconnecting: %v", err)
	} else {
		ce.Reply("Successfully reconnected")
	}
}

var cmdRejoinSpace = &commands.FullHandler{
	Func: wrapCommand(fnRejoinSpace),
	Name: "rejoin-space",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionUnclassified,
		Description: "Ask the bridge for an invite to a space you left",
		Args:        "<_guild ID_/main/dms>",
	},
	RequiresLogin: true,
}

func fnRejoinSpace(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage**: `$cmdprefix rejoin-space <guild ID/main/dms>`")
		return
	}
	user := ce.User
	if ce.Args[0] == "main" {
		user.ensureInvited(nil, user.GetSpaceRoom(), false)
		ce.Reply("Invited you to your main space ([link](%s))", user.GetSpaceRoom().URI(ce.Bridge.AS.HomeserverDomain).MatrixToURL())
	} else if ce.Args[0] == "dms" {
		user.ensureInvited(nil, user.GetDMSpaceRoom(), false)
		ce.Reply("Invited you to your DM space ([link](%s))", user.GetDMSpaceRoom().URI(ce.Bridge.AS.HomeserverDomain).MatrixToURL())
	} else if _, err := strconv.Atoi(ce.Args[0]); err == nil {
		ce.Reply("Rejoining guild spaces is not yet implemented")
	} else {
		ce.Reply("**Usage**: `$cmdprefix rejoin-space <guild ID/main/dms>`")
		return
	}
}

var roomModerator = event.Type{Type: "fi.mau.discord.admin", Class: event.StateEventType}

var cmdSetRelay = &commands.FullHandler{
	Func: wrapCommand(fnSetRelay),
	Name: "set-relay",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionUnclassified,
		Description: "Create or set a relay webhook for a portal",
		Args:        "[room ID] <​--url URL> OR <​--create [name]>",
	},
	RequiresLogin:      true,
	RequiresEventLevel: roomModerator,
}

const webhookURLFormat = "https://discord.com/api/webhooks/%d/%s"

const selectRelayHelp = "Usage: `$cmdprefix [room ID] <​--url URL> OR <​--create [name]>`"

func fnSetRelay(ce *WrappedCommandEvent) {
	portal := ce.Portal
	if len(ce.Args) > 0 && strings.HasPrefix(ce.Args[0], "!") {
		portal = ce.Bridge.GetPortalByMXID(id.RoomID(ce.Args[0]))
		if portal == nil {
			ce.Reply("Portal with room ID %s not found", ce.Args[0])
			return
		}
		levels, err := portal.MainIntent().PowerLevels(ce.RoomID)
		if err != nil {
			ce.ZLog.Warn().Err(err).Msg("Failed to check room power levels")
			ce.Reply("Failed to get room power levels to see if you're allowed to use that command")
			return
		} else if levels.GetUserLevel(ce.User.GetMXID()) < levels.GetEventLevel(roomModerator) {
			ce.Reply("You don't have admin rights in that room")
			return
		}
		ce.Args = ce.Args[1:]
	} else if portal == nil {
		ce.Reply("You must either run the command in a portal, or specify an internal room ID as the first parameter")
		return
	}
	if ce.Portal.GuildID == "" {
		ce.Reply("Only guild channels can have relays")
		return
	} else if len(ce.Args) == 0 {
		ce.Reply(selectRelayHelp)
		return
	}
	log := ce.ZLog.With().Str("channel_id", portal.Key.ChannelID).Logger()
	createType := strings.ToLower(strings.TrimLeft(ce.Args[0], "-"))
	var webhookMeta *discordgo.Webhook
	switch createType {
	case "url":
		if len(ce.Args) < 2 {
			ce.Reply("Usage: `$cmdprefix [room ID] --url <URL>")
			return
		}
		ce.Redact()
		var webhookID int64
		var webhookSecret string
		_, err := fmt.Sscanf(ce.Args[1], webhookURLFormat, &webhookID, &webhookSecret)
		if err != nil {
			log.Warn().Str("webhook_url", ce.Args[1]).Err(err).Msg("Failed to parse provided webhook URL")
			ce.Reply("Invalid webhook URL")
			return
		}
		webhookMeta, err = relayClient.WebhookWithToken(strconv.FormatInt(webhookID, 10), webhookSecret)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get webhook info")
			ce.Reply("Failed to get webhook info: %v", err)
			return
		}
	case "create":
		perms, err := ce.User.Session.UserChannelPermissions(ce.User.DiscordID, portal.Key.ChannelID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to check user permissions")
			ce.Reply("Failed to check if you have permission to create webhooks")
			return
		} else if perms&discordgo.PermissionManageWebhooks == 0 {
			log.Debug().Int64("perms", perms).Msg("User doesn't have permissions to manage webhooks in channel")
			ce.Reply("You don't have permission to manage webhooks in that channel")
			return
		}
		name := "mautrix"
		if len(ce.Args) > 1 {
			name = strings.Join(ce.Args[1:], " ")
		}
		log.Debug().Str("webhook_name", name).Msg("Creating webhook")
		webhookMeta, err = ce.User.Session.WebhookCreate(portal.Key.ChannelID, name, "")
		if err != nil {
			log.Warn().Err(err).Msg("Failed to create webhook")
			ce.Reply("Failed to create webhook: %v", err)
			return
		}
	default:
		ce.Reply(selectRelayHelp)
		return
	}
	if portal.Key.ChannelID != webhookMeta.ChannelID {
		log.Debug().
			Str("portal_channel_id", portal.Key.ChannelID).
			Str("webhook_channel_id", webhookMeta.ChannelID).
			Msg("Provided webhook is for wrong channel")
		ce.Reply("That webhook is not for the right channel (expected %s, webhook is for %s)", portal.Key.ChannelID, webhookMeta.ChannelID)
		return
	}
	log.Debug().Str("webhook_id", webhookMeta.ID).Msg("Setting portal relay webhook")
	portal.RelayWebhookID = webhookMeta.ID
	portal.RelayWebhookSecret = webhookMeta.Token
	portal.Update()
	ce.Reply("Saved webhook %s (%s) as portal relay webhook", webhookMeta.Name, portal.RelayWebhookID)
}

var cmdUnsetRelay = &commands.FullHandler{
	Func: wrapCommand(fnUnsetRelay),
	Name: "unset-relay",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionUnclassified,
		Description: "Disable the relay webhook and optionally delete it on Discord",
		Args:        "[--delete]",
	},
	RequiresPortal:     true,
	RequiresEventLevel: roomModerator,
}

func fnUnsetRelay(ce *WrappedCommandEvent) {
	if ce.Portal.RelayWebhookID == "" {
		ce.Reply("This portal doesn't have a relay webhook")
		return
	}
	if len(ce.Args) > 0 && ce.Args[0] == "--delete" {
		err := relayClient.WebhookDeleteWithToken(ce.Portal.RelayWebhookID, ce.Portal.RelayWebhookSecret)
		if err != nil {
			ce.Reply("Failed to delete webhook: %v", err)
			return
		} else {
			ce.Reply("Successfully deleted webhook")
		}
	} else {
		ce.Reply("Relay webhook disabled")
	}
	ce.Portal.RelayWebhookID = ""
	ce.Portal.RelayWebhookSecret = ""
	ce.Portal.Update()
}

var cmdGuilds = &commands.FullHandler{
	Func:    wrapCommand(fnGuilds),
	Name:    "guilds",
	Aliases: []string{"servers", "guild", "server"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionUnclassified,
		Description: "Guild bridging management",
		Args:        "<status/bridge/unbridge/bridging-mode> [_guild ID_] [...]",
	},
	RequiresLogin: true,
}

const smallGuildsHelp = "**Usage**: `$cmdprefix guilds <help/status/bridge/unbridge> [guild ID] [...]`"

const fullGuildsHelp = smallGuildsHelp + `

* **help** - View this help message.
* **status** - View the list of guilds and their bridging status.
* **bridge <_guild ID_> [--entire]** - Enable bridging for a guild. The --entire flag auto-creates portals for all channels.
* **bridging-mode <_guild ID_> <_mode_>** - Set the mode for bridging messages and new channels in a guild.
* **unbridge <_guild ID_>** - Unbridge a guild and delete all channel portal rooms.`

func fnGuilds(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply(fullGuildsHelp)
		return
	}
	subcommand := strings.ToLower(ce.Args[0])
	ce.Args = ce.Args[1:]
	switch subcommand {
	case "status", "list":
		fnListGuilds(ce)
	case "bridge":
		fnBridgeGuild(ce)
	case "unbridge", "delete":
		fnUnbridgeGuild(ce)
	case "bridging-mode", "mode":
		fnGuildBridgingMode(ce)
	case "help":
		ce.Reply(fullGuildsHelp)
	default:
		ce.Reply("Unknown subcommand `%s`\n\n"+smallGuildsHelp, subcommand)
	}
}

func fnListGuilds(ce *WrappedCommandEvent) {
	var items []string
	for _, userGuild := range ce.User.GetPortals() {
		guild := ce.Bridge.GetGuildByID(userGuild.DiscordID, false)
		if guild == nil {
			continue
		}
		var avatarHTML string
		if !guild.AvatarURL.IsEmpty() {
			avatarHTML = fmt.Sprintf(`<img data-mx-emoticon height="24" src="%s" alt="" title="Guild avatar"> `, guild.AvatarURL.String())
		}
		items = append(items, fmt.Sprintf("<li>%s%s (<code>%s</code>) - %s</li>", avatarHTML, html.EscapeString(guild.Name), guild.ID, guild.BridgingMode.Description()))
	}
	if len(items) == 0 {
		ce.Reply("No guilds found")
	} else {
		ce.ReplyAdvanced(fmt.Sprintf("<p>List of guilds:</p><ul>%s</ul>", strings.Join(items, "")), false, true)
	}
}

func fnBridgeGuild(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 || len(ce.Args) > 2 {
		ce.Reply("**Usage**: `$cmdprefix guilds bridge <guild ID> [--entire]")
	} else if err := ce.User.bridgeGuild(ce.Args[0], len(ce.Args) == 2 && strings.ToLower(ce.Args[1]) == "--entire"); err != nil {
		ce.Reply("Error bridging guild: %v", err)
	} else {
		ce.Reply("Successfully bridged guild")
	}
}

func fnUnbridgeGuild(ce *WrappedCommandEvent) {
	if len(ce.Args) != 1 {
		ce.Reply("**Usage**: `$cmdprefix guilds unbridge <guild ID>")
	} else if err := ce.User.unbridgeGuild(ce.Args[0]); err != nil {
		ce.Reply("Error unbridging guild: %v", err)
	} else {
		ce.Reply("Successfully unbridged guild")
	}
}

const availableModes = "Available modes:\n" +
	"* `nothing` to never bridge any messages (default when unbridged)\n" +
	"* `if-portal-exists` to bridge messages in existing portals, but drop messages in unbridged channels\n" +
	"* `create-on-message` to bridge all messages and create portals if necessary on incoming messages (default after bridging)\n" +
	"* `everything` to bridge all messages and create portals proactively on bridge startup (default if bridged with `--entire`)\n"

func fnGuildBridgingMode(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 || len(ce.Args) > 2 {
		ce.Reply("**Usage**: `$cmdprefix guilds bridging-mode <guild ID> [mode]`\n\n" + availableModes)
		return
	}
	guild := ce.Bridge.GetGuildByID(ce.Args[0], false)
	if guild == nil {
		ce.Reply("Guild not found")
		return
	}
	if len(ce.Args) == 1 {
		ce.Reply("%s (%s) is currently set to %s (`%s`)\n\n%s", guild.PlainName, guild.ID, guild.BridgingMode.Description(), guild.BridgingMode.String(), availableModes)
		return
	}
	mode := database.ParseGuildBridgingMode(ce.Args[1])
	if mode == database.GuildBridgeInvalid {
		ce.Reply("Invalid guild bridging mode `%s`", ce.Args[1])
		return
	}
	guild.BridgingMode = mode
	guild.Update()
	ce.Reply("Set guild bridging mode to %s", mode.Description())
}

var cmdDeleteAllPortals = &commands.FullHandler{
	Func: wrapCommand(fnDeleteAllPortals),
	Name: "delete-all-portals",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionUnclassified,
		Description: "Delete all portals.",
	},
	RequiresAdmin: true,
}

func fnDeleteAllPortals(ce *WrappedCommandEvent) {
	portals := ce.Bridge.GetAllPortals()
	guilds := ce.Bridge.GetAllGuilds()
	if len(portals) == 0 && len(guilds) == 0 {
		ce.Reply("Didn't find any portals")
		return
	}

	leave := func(mxid id.RoomID, intent *appservice.IntentAPI) {
		if len(mxid) > 0 {
			_, _ = intent.KickUser(mxid, &mautrix.ReqKickUser{
				Reason: "Deleting portal",
				UserID: ce.User.MXID,
			})
		}
	}
	customPuppet := ce.Bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		intent := customPuppet.CustomIntent()
		leave = func(mxid id.RoomID, _ *appservice.IntentAPI) {
			if len(mxid) > 0 {
				_, _ = intent.LeaveRoom(mxid)
				_, _ = intent.ForgetRoom(mxid)
			}
		}
	}
	ce.Reply("Found %d channel portals and %d guild portals, deleting...", len(portals), len(guilds))
	for _, portal := range portals {
		portal.Delete()
		leave(portal.MXID, portal.MainIntent())
	}
	for _, guild := range guilds {
		guild.Delete()
		leave(guild.MXID, ce.Bot)
	}
	ce.Reply("Finished deleting portal info. Now cleaning up rooms in background.")

	go func() {
		for _, portal := range portals {
			portal.cleanup(false)
		}
		ce.Reply("Finished background cleanup of deleted portal rooms.")
	}()
}
