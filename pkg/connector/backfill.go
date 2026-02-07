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
	"slices"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var (
	_ bridgev2.BackfillingNetworkAPI = (*DiscordClient)(nil)
)

func (dc *DiscordClient) FetchMessages(ctx context.Context, fetchParams bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if !dc.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}

	channelID := discordid.ParsePortalID(fetchParams.Portal.ID)
	log := zerolog.Ctx(ctx).With().
		Str("action", "fetch messages").
		Str("channel_id", channelID).
		Int("desired_count", fetchParams.Count).
		Bool("forward", fetchParams.Forward).Logger()
	ctx = log.WithContext(ctx)

	var beforeID string
	var afterID string

	if fetchParams.AnchorMessage != nil {
		anchorID := discordid.ParseMessageID(fetchParams.AnchorMessage.ID)

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

	// Update our user cache with all of the users present in the response. This
	// indirectly makes `GetUserInfo` on `DiscordClient` return the information
	// we've fetched above.
	cachedDiscordUserIDs := dc.userCache.UpdateWithMessages(msgs)

	{
		log := zerolog.Ctx(ctx).With().
			Str("action", "update ghosts via fetched messages").
			Logger()
		ctx := log.WithContext(ctx)

		// Update/create all of the ghosts for the users involved. This lets us
		// set a correct per-message profile on each message, even for users
		// that we've never seen until now.
		for _, discordUserID := range cachedDiscordUserIDs {

			ghost, err := dc.connector.Bridge.GetGhostByID(ctx, discordid.MakeUserID(discordUserID))
			if err != nil {
				log.Err(err).Str("ghost_id", discordUserID).
					Msg("Failed to get ghost associated with message")
				continue
			}
			ghost.UpdateInfoIfNecessary(ctx, dc.UserLogin, bridgev2.RemoteEventMessage)
		}
	}

	converted := make([]*bridgev2.BackfillMessage, 0, len(msgs))
	for _, msg := range msgs {
		streamOrder, _ := strconv.ParseInt(msg.ID, 10, 64)
		ts, _ := discordgo.SnowflakeTimestamp(msg.ID)

		// NOTE: For now, we aren't backfilling reactions. This is because:
		//
		// - Discord does not provide enough historical reaction data in the
		//	 response from the message history endpoint to construct valid
		//   BackfillReactions.
		// - Fetching the reaction data would be prohibitively expensive for
		//   messages with many reactions. Messages in large guilds can have
		//   tens of thousands of reactions.
		// - Indicating aggregated child events[1] from BackfillMessage doesn't
		//   seem possible due to how portal backfilling batching currently
		//   works.
		//
		// [1]: https://spec.matrix.org/v1.16/client-server-api/#reference-relations
		//
		// It might be worth fetching the reaction data anyways if we observe
		// a small overall number of reactions.
		sender := dc.makeEventSender(msg.Author)

		// Use the ghost's intent, falling back to the bridge's.
		ghost, err := dc.connector.Bridge.GetGhostByID(ctx, sender.Sender)
		if err != nil {
			log.Err(err).Msg("Failed to look up ghost while converting backfilled message")
		}
		var intent bridgev2.MatrixAPI
		if ghost == nil {
			intent = fetchParams.Portal.Bridge.Bot
		} else {
			intent = ghost.Intent
		}

		converted = append(converted, &bridgev2.BackfillMessage{
			ID:               discordid.MakeMessageID(msg.ID),
			ConvertedMessage: dc.connector.MsgConv.ToMatrix(ctx, fetchParams.Portal, intent, dc.UserLogin, dc.Session, msg),
			Sender:           sender,
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
