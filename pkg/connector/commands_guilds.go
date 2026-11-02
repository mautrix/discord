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
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var cmdGuilds = &commands.FullHandler{
	Name: "guilds", Aliases: []string{"servers", "guild", "server"},
	Help:          commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Manage Discord guild bridging.", Args: "<status/bridge/unbridge/bridging-mode> [guild ID] [...]"},
	RequiresLogin: true,
	Func:          fnGuilds,
}

const guildsUsage = "Usage: `$cmdprefix guilds <status/bridge/unbridge/bridging-mode> [guild ID] [...]`\n\n" +
	"* `bridge <guild ID> [--entire]`: bridge a guild, optionally creating all channel portals.\n" +
	"* `bridging-mode <guild ID> [mode]`: view or set nothing, if-portal-exists, create-on-message or everything.\n" +
	"* `unbridge <guild ID>`: unbridge the guild and delete its portal rooms."

func fnGuilds(ce *commands.Event) {
	if len(ce.Args) == 0 || ce.Args[0] == "help" {
		ce.Reply(guildsUsage)
		return
	}
	subcommand := strings.ToLower(ce.Args[0])
	client := commandClient(ce, subcommand != "unbridge" && subcommand != "delete")
	if client == nil {
		return
	}
	if subcommand == "status" || subcommand == "list" {
		client.Session.State.RLock()
		guilds := slices.Clone(client.Session.State.Guilds)
		client.Session.State.RUnlock()
		var entries []string
		for _, guild := range guilds {
			entries = append(entries, fmt.Sprintf("* %s (`%s`) - %s", guild.Name, guild.ID, client.guildBridgingMode(guild.ID).Description()))
		}
		if len(entries) == 0 {
			ce.Reply("No guilds found")
		} else {
			ce.Reply("%s", strings.Join(entries, "\n"))
		}
		return
	}
	if len(ce.Args) < 2 || len(ce.Args) > 3 || !isSnowflake(ce.Args[1]) {
		ce.Reply(guildsUsage)
		return
	}
	guildID := ce.Args[1]
	if subcommand == "unbridge" || subcommand == "delete" {
		if len(ce.Args) != 2 {
			ce.Reply(guildsUsage)
			return
		}
		if _, err := client.unbridgeGuild(context.WithoutCancel(ce.Ctx), guildID); err != nil {
			ce.Reply("Error unbridging guild: %v", err)
		} else {
			ce.Reply("Successfully unbridged guild")
		}
		return
	}
	guild, err := client.Session.State.Guild(guildID)
	if err != nil {
		ce.Reply("Guild not found")
		return
	}
	var mode discordid.GuildBridgingMode
	switch subcommand {
	case "bridge":
		mode = discordid.GuildBridgeCreateOnMessage
		if len(ce.Args) == 3 {
			if ce.Args[2] != "--entire" {
				ce.Reply(guildsUsage)
				return
			}
			mode = discordid.GuildBridgeEverything
		}
	case "bridging-mode", "mode":
		if len(ce.Args) == 2 {
			ce.Reply("%s (`%s`) is set to `%s`", guild.Name, guildID, client.guildBridgingMode(guildID))
			return
		}
		mode = discordid.ParseGuildBridgingMode(ce.Args[2])
		if mode == discordid.GuildBridgeInvalid {
			ce.Reply(guildsUsage)
			return
		}
	default:
		ce.Reply(guildsUsage)
		return
	}
	if err = client.setGuildBridgingMode(ce.Ctx, guildID, mode); err == nil && mode != discordid.GuildBridgeNothing {
		err = client.syncGuild(ce.Ctx, guildID)
	}
	if err != nil {
		ce.Reply("Error bridging guild: %v", err)
	} else {
		ce.Reply("Set guild bridging mode to %s", mode.Description())
	}
}
