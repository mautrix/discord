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
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/util/variationselector"

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
	parentChannelID := discordid.ParseChannelPortalID(portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	threadRootRemoteID := getMatrixThreadRootRemoteMessageID(msg.ThreadRoot)

	if threadRootRemoteID != "" {
		thread, err := d.getThreadByRootMessageID(ctx, threadRootRemoteID)
		if err != nil {
			return nil, err
		}
		if thread != nil {
			threadChannelID = thread.ThreadChannelID
		} else if guildID != "" {
			var startErr error
			threadChannelID, startErr = d.startThreadFromMatrix(ctx, guildID, parentChannelID, threadRootRemoteID, getThreadName(msg.Content))
			if startErr != nil {
				// If creating the thread failed, try resolving it once more in case it already exists.
				thread, err = d.getThreadByRootMessageID(ctx, threadRootRemoteID)
				if err != nil {
					return nil, err
				} else if thread != nil {
					threadChannelID = thread.ThreadChannelID
				} else {
					return nil, fmt.Errorf("failed to create Discord thread from Matrix message: %w", startErr)
				}
			}
		}
	}
	if threadChannelID != "" {
		channelID = threadChannelID
	}
	refererOpt := makeDiscordReferer(guildID, parentChannelID, threadChannelID)

	sendReq, err := d.connector.MsgConv.ToDiscord(ctx, d.Session, msg, channelID, refererOpt)
	if err != nil {
		return nil, err
	}

	if sendReq.Reference != nil && sendReq.Reference.ChannelID == parentChannelID && threadChannelID != "" {
		sendReq.Reference.ChannelID = threadChannelID
	}

	sentMsg, err := d.Session.ChannelMessageSendComplex(channelID, sendReq, refererOpt)
	if err != nil {
		return nil, err
	}
	sentMsgTimestamp, _ := discordgo.SnowflakeTimestamp(sentMsg.ID)
	dbMessage := &database.Message{
		ID:        discordid.MakeMessageID(sentMsg.ID),
		SenderID:  discordid.MakeUserID(sentMsg.Author.ID),
		Timestamp: sentMsgTimestamp,
	}
	if threadRootRemoteID != "" {
		dbMessage.ThreadRoot = discordid.MakeMessageID(threadRootRemoteID)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: dbMessage,
	}, nil
}

func (d *DiscordClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	log := zerolog.Ctx(ctx).With().Str("action", "matrix message edit").Logger()
	ctx = log.WithContext(ctx)

	content, _ := d.connector.MsgConv.ConvertMatrixMessageContent(
		ctx,
		msg.Portal,
		msg.Content,
		// Disregard link previews for now. Discord generally allows you to
		// remove individual link previews from a message though.
		[]string{},
	)

	guildID := msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	parentChannelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	if msg.EditTarget != nil && msg.EditTarget.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(msg.EditTarget.ThreadRoot))
		if err != nil {
			return fmt.Errorf("failed to resolve target thread for message edit: %w", err)
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}

	_, err := d.Session.ChannelMessageEdit(
		channelID,
		discordid.ParseMessageID(msg.EditTarget.ID),
		content,
		makeDiscordReferer(guildID, parentChannelID, threadChannelID),
	)
	if err != nil {
		return fmt.Errorf("failed to send message edit to discord: %w", err)
	}

	return nil
}

func (d *DiscordClient) PreHandleMatrixReaction(ctx context.Context, reaction *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emojiID := reaction.Content.RelatesTo.Key

	// Figure out if this is a custom emoji or not.
	if strings.HasPrefix(emojiID, "mxc://") {
		customEmoji, err := d.connector.GetCustomEmojiByMXC(ctx, emojiID)

		if err != nil {
			return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("failed to get custom emoji by mxc: %w", err)
		} else if customEmoji == nil || customEmoji.ID == "" || customEmoji.Name == "" {
			return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("unknown custom emoji mxc: %q", emojiID)
		}

		emojiID = fmt.Sprintf("%s:%s", customEmoji.Name, customEmoji.ID)
	} else {
		emojiID = variationselector.FullyQualify(emojiID)
	}

	return bridgev2.MatrixReactionPreResponse{
		SenderID: discordid.UserLoginIDToUserID(d.UserLogin.ID),
		EmojiID:  discordid.MakeEmojiID(emojiID),
	}, nil
}

func (d *DiscordClient) HandleMatrixReaction(ctx context.Context, reaction *bridgev2.MatrixReaction) (*database.Reaction, error) {
	portal := reaction.Portal
	meta := portal.Metadata.(*discordid.PortalMetadata)
	parentChannelID := discordid.ParseChannelPortalID(portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	if reaction.TargetMessage != nil && reaction.TargetMessage.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(reaction.TargetMessage.ThreadRoot))
		if err != nil {
			return nil, err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}

	err := d.Session.MessageReactionAddUser(
		meta.GuildID,
		channelID,
		discordid.ParseMessageID(reaction.TargetMessage.ID),
		discordid.ParseEmojiID(reaction.PreHandleResp.EmojiID),
		makeDiscordReferer(meta.GuildID, parentChannelID, threadChannelID),
	)
	return nil, err
}

func (d *DiscordClient) HandleMatrixReactionRemove(ctx context.Context, removal *bridgev2.MatrixReactionRemove) error {
	removing := removal.TargetReaction
	emojiID := removing.EmojiID
	parentChannelID := discordid.ParseChannelPortalID(removal.Portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	guildID := removal.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	targetMessage, err := d.UserLogin.Bridge.DB.Message.GetFirstPartByID(ctx, d.UserLogin.ID, removing.MessageID)
	if err != nil {
		return err
	}
	if targetMessage != nil && targetMessage.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(targetMessage.ThreadRoot))
		if err != nil {
			return err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}

	err = d.Session.MessageReactionRemoveUser(
		guildID,
		channelID,
		discordid.ParseMessageID(removing.MessageID),
		discordid.ParseEmojiID(emojiID),
		discordid.ParseUserLoginID(d.UserLogin.ID),
		makeDiscordReferer(guildID, parentChannelID, threadChannelID),
	)
	return err
}

func (d *DiscordClient) HandleMatrixMessageRemove(ctx context.Context, removal *bridgev2.MatrixMessageRemove) error {
	guildID := removal.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	parentChannelID := discordid.ParseChannelPortalID(removal.Portal.ID)
	channelID := parentChannelID
	threadChannelID := ""
	if removal.TargetMessage != nil && removal.TargetMessage.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(removal.TargetMessage.ThreadRoot))
		if err != nil {
			return err
		} else if thread != nil {
			threadChannelID = thread.ThreadChannelID
			channelID = threadChannelID
		}
	}
	messageID := discordid.ParseMessageID(removal.TargetMessage.ID)
	return d.Session.ChannelMessageDelete(channelID, messageID, makeDiscordReferer(guildID, parentChannelID, threadChannelID))
}

func (d *DiscordClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	log := msg.Portal.Log.With().
		Str("event_id", string(msg.EventID)).
		Str("action", "matrix read receipt").Logger()

	guildID := msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	parentChannelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	threadChannelID := ""
	threadRootRemoteID := ""
	threadID := msg.Receipt.ThreadID
	threadScoped := threadID != "" && threadID != event.ReadReceiptThreadMain

	if threadScoped {
		rootMsg, err := d.UserLogin.Bridge.DB.Message.GetPartByMXID(ctx, threadID)
		if err != nil {
			log.Err(err).Msg("Failed to resolve thread root event from receipt")
			return err
		} else if rootMsg != nil {
			threadRootRemoteID = discordid.ParseMessageID(rootMsg.ID)
			if rootMsg.ThreadRoot != "" {
				threadRootRemoteID = discordid.ParseMessageID(rootMsg.ThreadRoot)
			}
			thread, err := d.getThreadByRootMessageID(ctx, threadRootRemoteID)
			if err != nil {
				log.Err(err).Msg("Failed to resolve thread channel from thread root")
				return err
			} else if thread != nil {
				threadChannelID = thread.ThreadChannelID
			}
		}
	}
	if threadScoped && threadRootRemoteID == "" {
		log.Debug().Stringer("receipt_thread_id", threadID).Msg("Dropping thread-scoped read receipt: unknown thread root")
		return nil
	}

	var targetMessage *database.Message
	var targetMessageID string

	// Figure out the ID of the Discord message that we'll mark as read. If the
	// receipt didn't exactly correspond with a message, try finding one close
	// by to use as the target.
	if msg.ExactMessage != nil {
		targetMessage = msg.ExactMessage
		targetMessageID = discordid.ParseMessageID(targetMessage.ID)
		log = log.With().
			Str("message_id", targetMessageID).
			Logger()
	} else {
		var err error
		if threadScoped && threadRootRemoteID != "" {
			targetMessage, err = d.UserLogin.Bridge.DB.Message.GetLastThreadMessage(ctx, msg.Portal.PortalKey, discordid.MakeMessageID(threadRootRemoteID))
			if err != nil {
				log.Err(err).Msg("Failed to find latest thread message")
				return err
			}
			if targetMessage != nil && targetMessage.Timestamp.After(msg.ReadUpTo) {
				targetMessage = nil
			}
		} else {
			targetMessage, err = d.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
			if err != nil {
				log.Err(err).Msg("Failed to find closest message part")
				return err
			}
		}

		if targetMessage != nil {
			// The read receipt didn't specify an exact message but we were able to
			// find one close by.

			targetMessageID = discordid.ParseMessageID(targetMessage.ID)
			log = log.With().
				Str("closest_message_id", targetMessageID).
				Str("closest_event_id", targetMessage.MXID.String()).
				Logger()
			log.Debug().
				Msg("Read receipt target event not found, using closest message")
		} else {
			log.Debug().Msg("Dropping read receipt: no messages found")
			return nil
		}
	}

	if threadScoped && targetMessage != nil {
		targetMsgThreadRoot := discordid.ParseMessageID(targetMessage.ThreadRoot)
		if targetMsgThreadRoot == "" {
			targetMsgThreadRoot = discordid.ParseMessageID(targetMessage.ID)
		}
		if threadRootRemoteID != "" && targetMsgThreadRoot != threadRootRemoteID {
			log.Debug().
				Str("receipt_thread_root", threadRootRemoteID).
				Str("target_thread_root", targetMsgThreadRoot).
				Msg("Dropping read receipt due to thread mismatch")
			return nil
		}
		if threadChannelID == "" && targetMsgThreadRoot != "" {
			thread, err := d.getThreadByRootMessageID(ctx, targetMsgThreadRoot)
			if err != nil {
				return err
			} else if thread != nil {
				threadChannelID = thread.ThreadChannelID
			}
		}
	}

	channelID := parentChannelID
	if threadChannelID != "" {
		channelID = threadChannelID
	}
	resp, err := d.Session.ChannelMessageAckNoToken(
		channelID,
		targetMessageID,
		makeDiscordReferer(guildID, parentChannelID, threadChannelID),
	)
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

	channelID := discordid.ParseChannelPortalID(portal.ID)
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
	channelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	err := d.Session.ChannelTyping(channelID, makeDiscordReferer(guildID, channelID, ""))

	if err != nil {
		log.Warn().Err(err).Msg("Failed to mark user as typing")
		return err
	}

	log.Debug().Msg("Marked user as typing")
	return nil
}
