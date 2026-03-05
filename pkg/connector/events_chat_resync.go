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
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type DiscordChatResync struct {
	Client  *DiscordClient
	channel *discordgo.Channel
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
	ch := d.channel
	return d.Client.portalKeyForChannel(ch)
}

func (d *DiscordChatResync) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{}
}

func (d *DiscordChatResync) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventChatResync
}

func (d *DiscordChatResync) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return d.Client.GetChatInfo(ctx, portal)

}

func (d *DiscordChatResync) ShouldCreatePortal() bool {
	return true
}

// compareMessageIDs compares two Discord message IDs.
//
// If the first ID is lower, -1 is returned.
// If the second ID is lower, 1 is returned.
// If the IDs are equal, 0 is returned.
func compareMessageIDs(id1, id2 string) int {
	if id1 == id2 {
		return 0
	}
	if len(id1) < len(id2) {
		return -1
	} else if len(id2) < len(id1) {
		return 1
	}
	if id1 < id2 {
		return -1
	}
	return 1
}

func shouldBackfill(latestBridgedIDStr, latestIDFromServerStr string) bool {
	return compareMessageIDs(latestBridgedIDStr, latestIDFromServerStr) == -1
}

func (d *DiscordChatResync) CheckNeedsBackfill(ctx context.Context, latestBridged *database.Message) (bool, error) {
	log := zerolog.Ctx(ctx).With().
		Str("resyncing_channel_id", d.channel.ID).
		Str("resyncing_channel_last_message_id", d.channel.LastMessageID).
		Str("resyncing_guild_id", d.channel.GuildID).
		Bool("has_latest_bridged", latestBridged != nil).
		Logger()

	if latestBridged == nil {
		needsBackfill := d.channel.LastMessageID != ""
		log.Debug().Bool("needs_backfill", needsBackfill).Msg("Computed needs backfill")
		return needsBackfill, nil
	}

	needsBackfill := shouldBackfill(
		discordid.ParseMessageID(latestBridged.ID),
		d.channel.LastMessageID,
	)
	log.Debug().Bool("needs_backfill", needsBackfill).Msg("Computed needs backfill")
	return needsBackfill, nil
}
