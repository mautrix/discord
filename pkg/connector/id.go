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
	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func (d *DiscordClient) portalKeyForChannel(ch *discordgo.Channel) networkid.PortalKey {
	switch ch.Type {
	case discordgo.ChannelTypeDM:
		return d.dmChannelPortalKey(ch.ID)
	case discordgo.ChannelTypeGroupDM:
		return d.groupDMChannelPortalKey(ch.ID)
	default:
		return d.guildChannelPortalKey(ch.ID)
	}
}

func (d *DiscordClient) guildChannelPortalKey(channelID string) networkid.PortalKey {
	wantReceiver := d.connector.Bridge.Config.SplitPortals
	return discordid.MakeChannelPortalKey(channelID, d.UserLogin.ID, wantReceiver)
}

func (d *DiscordClient) groupDMChannelPortalKey(channelID string) networkid.PortalKey {
	// Same logic as guild channels (only specify a receiver when split portals
	// are enabled).
	return d.guildChannelPortalKey(channelID)
}

func (d *DiscordClient) dmChannelPortalKey(channelID string) networkid.PortalKey {
	// 1:1 DMs should _always_ have a receiver.
	return discordid.MakeChannelPortalKey(channelID, d.UserLogin.ID, true)
}

func (d *DiscordClient) guildPortalKey(guildID string) networkid.PortalKey {
	wantReceiver := d.connector.Bridge.Config.SplitPortals
	return discordid.MakeGuildPortalKey(guildID, d.UserLogin.ID, wantReceiver)
}
