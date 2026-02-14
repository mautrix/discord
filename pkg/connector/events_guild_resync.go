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

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type DiscordGuildResync struct {
	Client    *DiscordClient
	guild     *discordgo.Guild
	portalKey networkid.PortalKey
}

var (
	_ bridgev2.RemoteChatResyncWithInfo       = (*DiscordGuildResync)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*DiscordGuildResync)(nil)
)

func (d *DiscordGuildResync) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.Str("guild_id", d.guild.ID).Str("guild_name", d.guild.Name)
}

func (d *DiscordGuildResync) GetPortalKey() networkid.PortalKey {
	return d.portalKey
}

func (d *DiscordGuildResync) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{}
}

func (d *DiscordGuildResync) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventChatResync
}

func (d *DiscordGuildResync) ShouldCreatePortal() bool {
	return true
}

func (d *DiscordGuildResync) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return d.Client.GetChatInfo(ctx, portal)
}
