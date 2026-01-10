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
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
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
	guildID := portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := string(portal.ID)

	sendReq, err := d.connector.MsgConv.ToDiscord(ctx, d.Session, msg)
	if err != nil {
		return nil, err
	}

	var options []discordgo.RequestOption
	// TODO: When supporting threads (and not a bot user), send a thread referer.
	options = append(options, discordgo.WithChannelReferer(guildID, channelID))

	sentMsg, err := d.Session.ChannelMessageSendComplex(string(msg.Portal.ID), sendReq, options...)
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

func (d *DiscordClient) PreHandleMatrixReaction(ctx context.Context, reaction *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	key := reaction.Content.RelatesTo.Key
	// TODO: Handle custom emoji.

	return bridgev2.MatrixReactionPreResponse{
		SenderID: networkid.UserID(d.UserLogin.ID),
		EmojiID:  networkid.EmojiID(key),
	}, nil
}

func (d *DiscordClient) HandleMatrixReaction(ctx context.Context, reaction *bridgev2.MatrixReaction) (*database.Reaction, error) {
	relatesToKey := reaction.Content.RelatesTo.Key
	portal := reaction.Portal
	meta := portal.Metadata.(*discordid.PortalMetadata)

	err := d.Session.MessageReactionAddUser(meta.GuildID, string(portal.ID), string(reaction.TargetMessage.ID), relatesToKey)
	return nil, err
}

func (d *DiscordClient) HandleMatrixReactionRemove(ctx context.Context, removal *bridgev2.MatrixReactionRemove) error {
	removing := removal.TargetReaction
	emojiID := removing.EmojiID
	channelID := string(removing.Room.ID)
	guildID := removal.Portal.Metadata.(*discordid.PortalMetadata).GuildID

	err := d.Session.MessageReactionRemoveUser(guildID, channelID, string(removing.MessageID), string(emojiID), string(d.UserLogin.ID))
	return err
}

func (d *DiscordClient) HandleMatrixMessageRemove(ctx context.Context, removal *bridgev2.MatrixMessageRemove) error {
	channelID := string(removal.Portal.ID)
	messageID := string(removal.TargetMessage.ID)
	return d.Session.ChannelMessageDelete(channelID, messageID)
}

func (d *DiscordClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	// TODO: Support threads.
	log := msg.Portal.Log.With().
		Str("event_id", string(msg.EventID)).
		Str("action", "matrix read receipt").Logger()

	var targetMessageID string

	// Figure out the ID of the Discord message that we'll mark as read. If the
	// receipt didn't exactly correspond with a message, try finding one close
	// by to use as the target.
	if msg.ExactMessage != nil {
		targetMessageID = string(msg.ExactMessage.ID)
		log = log.With().
			Str("message_id", targetMessageID).
			Logger()
	} else {
		closestMessage, err := d.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)

		if err != nil {
			log.Err(err).Msg("Failed to find closest message part")
			return err
		} else if closestMessage != nil {
			// The read receipt didn't specify an exact message but we were able to
			// find one close by.

			targetMessageID = string(closestMessage.ID)
			log = log.With().
				Str("closest_message_id", targetMessageID).
				Str("closest_event_id", closestMessage.MXID.String()).
				Logger()
			log.Debug().
				Msg("Read receipt target event not found, using closest message")
		} else {
			log.Debug().Msg("Dropping read receipt: no messages found")
			return nil
		}
	}

	// TODO: Support threads.
	guildID := msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := string(msg.Portal.ID)
	resp, err := d.Session.ChannelMessageAckNoToken(channelID, targetMessageID, discordgo.WithChannelReferer(guildID, channelID))
	if err != nil {
		log.Err(err).Msg("Failed to send read receipt to Discord")
		return err
	} else if resp.Token != nil {
		log.Debug().
			Str("unexpected_resp_token", *resp.Token).
			Msg("Marked message as read on Discord (and got unexpected non-nil token)")
	} else {
		log.Debug().Msg("Marked message as read on Discord")
	}

	return nil
}

func (d *DiscordClient) viewingChannel(ctx context.Context, portal *bridgev2.Portal) error {
	if portal.Metadata.(*discordid.PortalMetadata).GuildID != "" {
		// Only private channels need this logic.
		return nil
	}

	d.markedOpenedLock.Lock()
	defer d.markedOpenedLock.Unlock()

	channelID := string(portal.ID)
	log := zerolog.Ctx(ctx).With().
		Str("channel_id", channelID).Logger()

	lastMarkedOpenedTs := d.markedOpened[channelID]
	if lastMarkedOpenedTs.IsZero() {
		d.markedOpened[channelID] = time.Now()

		err := d.Session.MarkViewing(channelID)

		if err != nil {
			log.Error().Err(err).Msg("Failed to mark user as viewing channel")
			return err
		}

		log.Trace().Msg("Marked channel as being viewed")
	} else {
		log.Trace().Str("channel_id", channelID).
			Msg("Already marked channel as viewed, not doing so")
	}

	return nil
}

func (d *DiscordClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	log := zerolog.Ctx(ctx)

	// Don't mind if this fails.
	_ = d.viewingChannel(ctx, msg.Portal)

	guildID := msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := string(msg.Portal.ID)
	// TODO: Support threads properly when sending the referer.
	err := d.Session.ChannelTyping(channelID, discordgo.WithChannelReferer(guildID, channelID))

	if err != nil {
		log.Warn().Err(err).Msg("Failed to mark user as typing")
		return err
	}

	log.Debug().Msg("Marked user as typing")
	return nil
}
