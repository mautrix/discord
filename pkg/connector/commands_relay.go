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

package connector

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var cmdSetRelay = &commands.FullHandler{
	Name: "set-relay", Func: fnSetRelay, RequiresLogin: true,
	Help: commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Create or set a relay webhook.", Args: "[room ID] <--url URL/--create [name]>"},
}
var cmdUnsetRelay = &commands.FullHandler{
	Name: "unset-relay", Func: fnUnsetRelay, RequiresPortal: true, RequiresEventLevel: roomModerator,
	Help: commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Disable the relay webhook, optionally deleting it on Discord.", Args: "[--delete]"},
}

func parseWebhookURL(raw string) (webhookID, token string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "discord.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid webhook URL")
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 5 || parts[1] != "api" || parts[2] != "webhooks" || !isSnowflake(parts[3]) || parts[4] == "" || strings.ContainsAny(parts[4], " \\?&#\r\n\t") {
		return "", "", fmt.Errorf("invalid webhook URL")
	}
	return parts[3], parts[4], nil
}

func (d *DiscordConnector) saveRelay(ctx context.Context, portal *bridgev2.Portal, login *bridgev2.UserLogin, webhookID, secret string) error {
	if login != nil && portal.Receiver != "" && portal.Receiver != login.ID {
		return fmt.Errorf("relay login must own this portal")
	}
	d.relayLock.Lock()
	meta := portal.Metadata.(*discordid.PortalMetadata)
	oldID, oldSecret, oldLogin, oldLoginID := meta.RelayWebhookID, meta.RelayWebhookSecret, portal.Relay, portal.RelayLoginID
	meta.RelayWebhookID, meta.RelayWebhookSecret = webhookID, secret
	portal.Relay = login
	portal.RelayLoginID = ""
	if login != nil {
		portal.RelayLoginID = login.ID
	}
	err := portal.Save(ctx)
	if err != nil {
		meta.RelayWebhookID, meta.RelayWebhookSecret, portal.Relay, portal.RelayLoginID = oldID, oldSecret, oldLogin, oldLoginID
	}
	d.relayLock.Unlock()
	if err == nil {
		if login == nil {
			login = oldLogin
		}
		if login != nil {
			portal.UpdateCapabilities(ctx, login, true)
		}
		portal.UpdateBridgeInfo(ctx)
	}
	return err
}

func fnSetRelay(ce *commands.Event) {
	connector := ce.Bridge.Network.(*DiscordConnector)
	portal := ce.Portal
	if len(ce.Args) > 0 && strings.HasPrefix(ce.Args[0], "!") {
		var err error
		portal, err = ce.Bridge.GetPortalByMXID(ce.Ctx, id.RoomID(ce.Args[0]))
		if err != nil {
			ce.Reply("Failed to find portal: %v", err)
			return
		}
		ce.Args = ce.Args[1:]
	}
	if len(ce.Args) > 0 && strings.TrimLeft(strings.ToLower(ce.Args[0]), "-") == "url" {
		ce.Redact()
	}
	if portal == nil {
		ce.Reply("Run this command in a portal or specify its room ID first.")
		return
	}
	if !canManagePortal(ce, portal.MXID) {
		return
	}
	meta := connector.relayMetadata(portal)
	if meta.GuildID == "" {
		ce.Reply("Only guild channels can have relays")
		return
	}
	if meta.RelayWebhookID != "" {
		hook, err := connector.relayClient.WebhookWithToken(meta.RelayWebhookID, meta.RelayWebhookSecret, discordgo.WithContext(ce.Ctx))
		if err != nil {
			ce.Reply("Failed to get existing webhook: %v", webhookError(err, meta.RelayWebhookSecret))
		} else {
			ce.Reply("This channel already has relay webhook %s (%s)", hook.Name, hook.ID)
		}
		return
	}
	if len(ce.Args) == 0 {
		ce.Reply("Usage: `$cmdprefix set-relay [room ID] <--url URL/--create [name]>`")
		return
	}
	eventCopy := *ce
	eventCopy.Portal = portal
	client := commandClient(&eventCopy, true)
	if client == nil {
		return
	}
	channelID := discordid.ParseChannelPortalID(portal.ID)
	var hook *discordgo.Webhook
	var err error
	var secret string
	switch strings.TrimLeft(strings.ToLower(ce.Args[0]), "-") {
	case "url":
		if len(ce.Args) != 2 {
			ce.Reply("Usage: `$cmdprefix set-relay [room ID] --url URL`")
			return
		}
		var webhookID string
		webhookID, secret, err = parseWebhookURL(ce.Args[1])
		if err != nil {
			ce.Reply("Invalid webhook URL")
			return
		}
		hook, err = connector.relayClient.WebhookWithToken(webhookID, secret, discordgo.WithContext(ce.Ctx))
	case "create":
		var perms int64
		perms, err = client.Session.UserChannelPermissions(discordid.ParseUserLoginID(client.UserLogin.ID), channelID, discordgo.WithContext(ce.Ctx))
		if err != nil {
			ce.Reply("Failed to check Discord permissions: %v", err)
			return
		}
		if perms&discordgo.PermissionManageWebhooks == 0 {
			ce.Reply("You don't have permission to manage webhooks in that channel")
			return
		}
		name := "mautrix"
		if len(ce.Args) > 1 {
			name = strings.Join(ce.Args[1:], " ")
		}
		hook, err = client.Session.WebhookCreate(channelID, name, "", discordgo.WithContext(ce.Ctx))
	default:
		ce.Reply("Usage: `$cmdprefix set-relay [room ID] <--url URL/--create [name]>`")
		return
	}
	if err != nil {
		ce.Reply("Failed to configure webhook: %v", webhookError(err, secret))
		return
	}
	if hook.ChannelID != channelID {
		ce.Reply("That webhook is for a different channel")
		return
	}
	if hook.Token == "" {
		hook.Token = secret
	}
	if hook.Type != discordgo.WebhookTypeIncoming || hook.Token == "" {
		ce.Reply("That webhook can't be used for relaying")
		return
	}
	if err = connector.saveRelay(ce.Ctx, portal, client.UserLogin, hook.ID, hook.Token); err != nil {
		ce.Reply("Failed to save relay: %v", err)
		return
	}
	ce.Reply("Saved webhook %s (%s) as portal relay webhook", hook.Name, hook.ID)
}

func fnUnsetRelay(ce *commands.Event) {
	if len(ce.Args) > 1 || (len(ce.Args) == 1 && ce.Args[0] != "--delete") {
		ce.Reply("Usage: `$cmdprefix unset-relay [--delete]`")
		return
	}
	connector := ce.Bridge.Network.(*DiscordConnector)
	meta := connector.relayMetadata(ce.Portal)
	if meta.RelayWebhookID == "" {
		ce.Reply("This portal doesn't have a relay webhook")
		return
	}
	if len(ce.Args) == 1 {
		if err := connector.relayClient.WebhookDeleteWithToken(meta.RelayWebhookID, meta.RelayWebhookSecret, discordgo.WithContext(ce.Ctx)); err != nil {
			ce.Reply("Failed to delete webhook: %v", webhookError(err, meta.RelayWebhookSecret))
			return
		}
	}
	if err := connector.saveRelay(ce.Ctx, ce.Portal, nil, "", ""); err != nil {
		ce.Reply("Failed to disable relay: %v", err)
	} else {
		ce.Reply("Relay webhook disabled")
	}
}
