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
	"slices"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

var (
	_ bridgev2.BackfillingNetworkAPI = (*DiscordClient)(nil)
)

func (dc *DiscordClient) FetchMessages(ctx context.Context, fetchParams bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if !dc.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}

	channelID := string(fetchParams.Portal.ID)
	log := zerolog.Ctx(ctx).With().
		Str("channel_id", channelID).
		Int("desired_count", fetchParams.Count).
		Bool("forward", fetchParams.Forward).Logger()

	var beforeID string
	var afterID string

	if fetchParams.AnchorMessage != nil {
		anchorID := string(fetchParams.AnchorMessage.ID)

		if fetchParams.Forward {
			afterID = anchorID
		} else {
			beforeID = anchorID
		}
	}

	// ChannelMessages returns messages ordered from newest to oldest.
	count := min(fetchParams.Count, 100)
	log.Debug().Msg("Fetching channel history for backfill")
	msgs, err := dc.Session.ChannelMessages(channelID, count, beforeID, afterID, "")
	if err != nil {
		return nil, err
	}

	converted := make([]*bridgev2.BackfillMessage, 0, len(msgs))
	for _, msg := range msgs {
		streamOrder, _ := strconv.ParseInt(msg.ID, 10, 64)
		ts, _ := discordgo.SnowflakeTimestamp(msg.ID)

		// FIXME(skip): Backfill reactions.
		converted = append(converted, &bridgev2.BackfillMessage{
			ConvertedMessage: dc.convertMessage(msg),
			ID:               networkid.MessageID(msg.ID),
			Sender:           dc.makeEventSender(msg.Author),
			Timestamp:        ts,
			StreamOrder:      streamOrder,
		})
	}
	// FetchMessagesResponse expects messages to always be ordered from oldest to newest.
	slices.Reverse(converted)

	log.Debug().Int("converted_count", len(converted)).Msg("Finished fetching and converting, returning backfill response")

	return &bridgev2.FetchMessagesResponse{
		Messages: converted,
		Forward:  fetchParams.Forward,
		// This might not actually be true if the channel's total number of messages is itself a multiple
		// of `count`, but that's probably okay.
		HasMore: len(msgs) == count,
	}, nil
}

func (dc *DiscordClient) convertMessage(msg *discordgo.Message) *bridgev2.ConvertedMessage {
	// FIXME(skip): This isn't bridging a lot of things (replies, forwards, voice messages, attachments, webhooks, embeds, etc.). Copy from main branch.

	var parts []*bridgev2.ConvertedMessagePart
	switch msg.Type {
	case discordgo.MessageTypeCall:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Content: &event.MessageEventContent{
				MsgType: event.MsgEmote,
				Body:    "started a call",
			},
		})
	case discordgo.MessageTypeGuildMemberJoin:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Content: &event.MessageEventContent{
				MsgType: event.MsgEmote,
				Body:    "joined the server",
			},
		})
	}

	if msg.Content != "" {
		// FIXME(skip): This needs to render into HTML.
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    msg.Content,
			},
		})
	}

	return &bridgev2.ConvertedMessage{
		// TODO(skip): Replies.
		Parts: parts,
	}
}
