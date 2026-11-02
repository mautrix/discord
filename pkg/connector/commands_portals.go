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
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var roomModerator = event.Type{Type: "fi.mau.discord.admin", Class: event.StateEventType}

func canManagePortal(ce *commands.Event, roomID id.RoomID) bool {
	if ce.User.Permissions.Admin {
		return true
	}
	levels, err := ce.Bridge.Matrix.GetPowerLevels(ce.Ctx, roomID)
	if errors.Is(err, mautrix.MNotFound) {
		return true
	}
	if err != nil {
		ce.Reply("Failed to check room permissions: %v", err)
		return false
	}
	if levels.GetUserLevel(ce.User.MXID) < levels.GetEventLevel(roomModerator) {
		ce.Reply("You don't have admin rights in that room")
		return false
	}
	return true
}

func parseBridgeArgs(args []string) (channelID string, replace, deleteOld bool, err error) {
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "--replace":
			replace = true
		case "--replace=delete":
			replace, deleteOld = true, true
		default:
			if channelID != "" || !isSnowflake(arg) {
				return "", false, false, fmt.Errorf("invalid channel ID or flag")
			}
			channelID = arg
		}
	}
	if channelID == "" {
		err = fmt.Errorf("missing channel ID")
	}
	return
}

var cmdBridge = &commands.FullHandler{
	Name:               "bridge",
	Help:               commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Bridge this room to a Discord channel.", Args: "[--replace[=delete]] <channel ID>"},
	RequiresEventLevel: roomModerator,
	Func:               fnBridge,
}

func fnBridge(ce *commands.Event) {
	if ce.Portal != nil {
		ce.Reply("This room is already bridged. Use `$cmdprefix unbridge` first.")
		return
	}
	channelID, replace, deleteOld, err := parseBridgeArgs(ce.Args)
	if err != nil {
		ce.Reply("Usage: `$cmdprefix bridge [--replace[=delete]] <channel ID>`")
		return
	}
	var client *DiscordClient
	var portal *bridgev2.Portal
	if login := ce.User.GetDefaultLogin(); login != nil {
		client = login.Client.(*DiscordClient)
		portal, err = ce.Bridge.GetExistingPortalByKey(ce.Ctx, networkid.PortalKey{ID: discordid.MakeChannelPortalIDWithID(channelID), Receiver: login.ID})
	} else {
		portal, err = ce.Bridge.GetExistingPortalByKey(ce.Ctx, networkid.PortalKey{ID: discordid.MakeChannelPortalIDWithID(channelID)})
	}
	if err != nil {
		ce.Reply("Failed to find channel: %v", err)
		return
	}
	if portal == nil {
		if client == nil || !client.IsLoggedIn() {
			ce.Reply("Channel not found")
			return
		}
		portal, err = client.portalForCommand(ce, channelID)
		if err != nil {
			ce.Reply("Failed to find channel: %v", err)
			return
		}
	}
	if portal.MXID != "" && (!replace || !canManagePortal(ce, portal.MXID)) {
		ce.Reply("That channel is already bridged. Use `--replace` or `--replace=delete` to replace it.")
		return
	}
	if client == nil {
		logins, lookupErr := ce.Bridge.DB.UserPortal.GetAllInPortal(ce.Ctx, portal.PortalKey)
		if lookupErr != nil {
			ce.Reply("Failed to find a login for this channel: %v", lookupErr)
			return
		}
		for _, up := range logins {
			if login := ce.Bridge.GetCachedUserLoginByID(up.LoginID); login != nil {
				client, _ = login.Client.(*DiscordClient)
				if client != nil && client.IsLoggedIn() {
					break
				}
			}
		}
	}
	if client == nil || !client.IsLoggedIn() {
		ce.Reply("No connected Discord login found for this channel")
		return
	}
	guildID := portal.Metadata.(*discordid.PortalMetadata).GuildID
	if guildID != "" && client.guildBridgingMode(guildID) == discordid.GuildBridgeNothing {
		if err = client.setGuildBridgingMode(ce.Ctx, guildID, discordid.GuildBridgeIfPortalExists); err != nil {
			ce.Reply("Failed to enable guild: %v", err)
			return
		}
	}
	info, err := client.GetChatInfo(ce.Ctx, portal)
	if err != nil {
		ce.Reply("Failed to get channel info: %v", err)
		return
	}
	oldRoom := portal.MXID
	err = portal.UpdateMatrixRoomID(ce.Ctx, ce.RoomID, bridgev2.UpdateMatrixRoomIDParams{
		FailIfMXIDSet: !replace, ChatInfo: info, ChatInfoSource: client.UserLogin,
	})
	if err != nil {
		ce.Reply("Failed to bridge room: %v", err)
		return
	}
	if oldRoom != "" {
		if err = ce.Bot.DeleteRoom(ce.Ctx, oldRoom, !deleteOld); err != nil {
			ce.Reply("Room bridged, but cleaning up the old room failed: %v", err)
			return
		}
	}
	ce.Reply("Room successfully bridged")
}

func (d *DiscordClient) portalForCommand(ce *commands.Event, channelID string) (*bridgev2.Portal, error) {
	ch, err := d.Session.Channel(channelID, discordgo.WithContext(ce.Ctx))
	if err != nil {
		return nil, err
	}
	switch ch.Type {
	case discordgo.ChannelTypeDM, discordgo.ChannelTypeGroupDM, discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews:
	default:
		return nil, fmt.Errorf("that channel can't be bridged")
	}
	if err = d.Session.State.ChannelAdd(ch); err != nil {
		return nil, err
	}
	portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, d.portalKeyForChannel(ch))
	if err != nil {
		return nil, err
	}
	meta := portal.Metadata.(*discordid.PortalMetadata)
	meta.GuildID, meta.ChannelType = ch.GuildID, &ch.Type
	return portal, portal.Save(ce.Ctx)
}

var cmdCreatePortal = &commands.FullHandler{
	Name:          "create-portal",
	Help:          commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Create a portal for a Discord channel.", Args: "<channel ID>"},
	RequiresLogin: true,
	Func: func(ce *commands.Event) {
		if len(ce.Args) != 1 || !isSnowflake(ce.Args[0]) {
			ce.Reply("Usage: `$cmdprefix create-portal <channel ID>`")
			return
		}
		client := commandClient(ce, true)
		if client == nil {
			return
		}
		portal, err := client.portalForCommand(ce, ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get channel: %v", err)
			return
		}
		guildID := portal.Metadata.(*discordid.PortalMetadata).GuildID
		if guildID != "" && client.guildBridgingMode(guildID) == discordid.GuildBridgeNothing {
			ce.Reply("That guild isn't bridged. Use `$cmdprefix guilds bridge %s` first.", guildID)
			return
		}
		if portal.MXID != "" {
			ce.Reply("That channel is already bridged: %s", portal.MXID.URI().MatrixToURL())
			return
		}
		if err = client.ensurePortal(ce.Ctx, portal.PortalKey, nil); err != nil {
			ce.Reply("Failed to create portal: %v", err)
		} else {
			ce.Reply("Portal created: %s", portal.MXID.URI().MatrixToURL())
		}
	},
}

func fnUnbridge(ce *commands.Event) {
	room := ce.Portal.MXID
	if err := ce.Portal.RemoveMXID(ce.Ctx); err != nil {
		ce.Reply("Failed to unbridge: %v", err)
		return
	}
	if err := ce.Bot.DeleteRoom(ce.Ctx, room, ce.Command == "unbridge"); err != nil {
		ce.Reply("Failed to clean up room: %v", err)
	}
}

var cmdUnbridge = &commands.FullHandler{
	Name: "unbridge", Func: fnUnbridge, RequiresPortal: true, RequiresEventLevel: roomModerator,
	Help: commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Unbridge this room."},
}
var cmdDeletePortal = &commands.FullHandler{
	Name: "delete-portal", Func: fnUnbridge, RequiresPortal: true, RequiresEventLevel: roomModerator,
	Help: commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Unbridge this room and kick Matrix users."},
}
