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
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

var (
	_ bridgev2.ReactionHandlingNetworkAPI    = (*DiscordClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*DiscordClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*DiscordClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*DiscordClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*DiscordClient)(nil)
)

func (d *DiscordClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	portal := msg.Portal
	channelID := string(portal.ID)

	// TODO: Support replies.

	sendReq, err := d.connector.MsgConv.ToDiscord(ctx, msg)
	if err != nil {
		return nil, err
	}

	var options []discordgo.RequestOption
	// TODO: When supporting threads (and not a bot user), send a thread referer.
	// TODO: Pass the guild ID when send messages in guild channels.
	options = append(options, discordgo.WithChannelReferer("", channelID))

	sentMsg, err := d.Session.ChannelMessageSendComplex(string(msg.Portal.ID), &sendReq, options...)
	if err != nil {
		return nil, err
	}
	sentMsgTimestamp, _ := discordgo.SnowflakeTimestamp(sentMsg.ID)

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(sentMsg.ID),
			SenderID:  networkid.UserID(sentMsg.Author.ID),
			Timestamp: sentMsgTimestamp,
		},
	}, nil
}

func (d *DiscordClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (reaction *database.Reaction, err error) {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	//TODO implement me
	panic("implement me")
}
