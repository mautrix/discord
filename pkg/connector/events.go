// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type DiscordChatResync struct {
	channel   *discordgo.Channel
	portalKey networkid.PortalKey
	info      *bridgev2.ChatInfo
}

var (
	_ bridgev2.RemoteChatResyncWithInfo       = (*DiscordChatResync)(nil)
	_ bridgev2.RemoteChatResyncBackfill       = (*DiscordChatResync)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*DiscordChatResync)(nil)
)

func (d *DiscordChatResync) AddLogContext(c zerolog.Context) zerolog.Context {
	c = c.Str("channel_id", d.channel.ID).Int("channel_type", int(d.channel.Type))
	return c
}

func (d *DiscordChatResync) GetPortalKey() networkid.PortalKey {
	return d.portalKey
}

func (d *DiscordChatResync) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{}
}

func (d *DiscordChatResync) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventChatResync
}

func (d *DiscordChatResync) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	if d.info == nil {
		return nil, nil
	}
	return d.info, nil
}

func (d *DiscordChatResync) ShouldCreatePortal() bool {
	return true
}

func (d *DiscordChatResync) CheckNeedsBackfill(ctx context.Context, latestBridged *database.Message) (bool, error) {
	if latestBridged == nil {
		zerolog.Ctx(ctx).Debug().Str("channel_id", d.channel.ID).Msg("Haven't bridged any messages at all, not forward backfilling")
		return false, nil
	}
	return latestBridged.ID < networkid.MessageID(d.channel.LastMessageID), nil
}
