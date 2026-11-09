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
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/msgconv"
)

var _ bridgev2.PollResultsFetchingNetworkAPI = (*DiscordClient)(nil)

func (d *DiscordClient) FetchPollResults(ctx context.Context, portal *bridgev2.Portal, msg *database.Message) (*bridgev2.PollResults, error) {
	if !d.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}

	meta := portal.Metadata.(*discordid.PortalMetadata)
	parentChannelID := discordid.ParseChannelPortalID(portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	if msg.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(msg.ThreadRoot))
		if err != nil {
			return nil, err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}

	messageID := discordid.ParseMessageID(msg.ID)
	messages, err := d.Session.ChannelMessages(channelID, 1, "", "", messageID, makeDiscordReferer(meta.GuildID, parentChannelID, threadChannelID))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch poll message: %w", d.tryWrappingError(ctx, err))
	}
	var poll *discordgo.Poll
	for _, discordMsg := range messages {
		if discordMsg.ID == messageID {
			poll = discordMsg.Poll
		}
	}
	if poll == nil {
		return nil, mautrix.MNotFound.WithMessage("Message is not a poll")
	}

	counts := make(map[int]*discordgo.PollAnswerCount)
	results := &bridgev2.PollResults{
		Closed:  poll.Expiry != nil && poll.Expiry.Before(time.Now()),
		Answers: make([]*bridgev2.PollAnswerResult, len(poll.Answers)),
	}
	if poll.Results != nil {
		results.Closed = results.Closed || poll.Results.Finalized
		for _, count := range poll.Results.AnswerCounts {
			counts[count.ID] = count
		}
	}
	for i, answer := range poll.Answers {
		result := &bridgev2.PollAnswerResult{ID: msgconv.PollAnswerID(answer.AnswerID)}
		if count, ok := counts[answer.AnswerID]; ok {
			result.Voters = count.Count
			result.Chosen = count.MeVoted
		}
		results.TotalVoters += result.Voters
		results.Answers[i] = result
	}
	return results, nil
}
