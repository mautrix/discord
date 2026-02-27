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
	"runtime/debug"
	"slices"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"

	"go.mau.fi/util/variationselector"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type DiscordEventMeta struct {
	Type       bridgev2.RemoteEventType
	PortalKey  networkid.PortalKey
	LogContext func(c zerolog.Context) zerolog.Context
}

func (em *DiscordEventMeta) AddLogContext(c zerolog.Context) zerolog.Context {
	if em.LogContext == nil {
		return c
	}
	c = em.LogContext(c)
	return c
}

func (em *DiscordEventMeta) GetType() bridgev2.RemoteEventType {
	return em.Type
}

func (em *DiscordEventMeta) GetPortalKey() networkid.PortalKey {
	return em.PortalKey
}

type DiscordMessage struct {
	*DiscordEventMeta
	Data         *discordgo.Message
	Client       *DiscordClient
	ThreadRootID *networkid.MessageID
}

func (m *DiscordMessage) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "convert discord edit").Logger()
	ctx = log.WithContext(ctx)

	// FIXME don't redundantly reupload attachments
	convertedEdit := m.Client.connector.MsgConv.ToMatrix(
		ctx,
		portal,
		intent,
		m.Client.UserLogin,
		m.Client.Session,
		m.Data,
		m.ThreadRootID,
	)

	// TODO this is really gross and relies on how we assign incrementing numeric
	// part ids. to return a semantically correct `ConvertedEdit` we should ditch
	// this system
	slices.SortStableFunc(existing, func(a *database.Message, b *database.Message) int {
		ai, _ := strconv.Atoi(string(a.PartID))
		bi, _ := strconv.Atoi(string(b.PartID))
		return ai - bi
	})

	if len(convertedEdit.Parts) != len(existing) {
		// FIXME support # of parts changing; triggerable by removing individual
		// attachments, etc.
		//
		// at the very least we can make this better by handling attachments,
		// which are always(?) at the end
		log.Warn().Int("n_parts_existing", len(existing)).Int("n_parts_after_edit", len(convertedEdit.Parts)).
			Msg("Ignoring message edit that changed number of parts")
		return nil, bridgev2.ErrIgnoringRemoteEvent
	}

	parts := make([]*bridgev2.ConvertedEditPart, 0, len(existing))
	for pi, part := range convertedEdit.Parts {
		parts = append(parts, part.ToEditPart(existing[pi]))
	}

	return &bridgev2.ConvertedEdit{
		ModifiedParts: parts,
	}, nil
}

var (
	_ bridgev2.RemoteMessage                  = (*DiscordMessage)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*DiscordMessage)(nil)
	_ bridgev2.RemoteEdit                     = (*DiscordMessage)(nil)
	_ bridgev2.RemoteMessageRemove            = (*DiscordMessage)(nil)
)

func (m *DiscordMessage) GetTargetMessage() networkid.MessageID {
	return discordid.MakeMessageID(m.Data.ID)
}

func (m *DiscordMessage) GetTransactionID() networkid.TransactionID {
	if m.Data.Nonce == "" {
		return ""
	}
	return networkid.TransactionID(m.Data.Nonce)
}

func (m *DiscordMessage) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return m.Client.connector.MsgConv.ToMatrix(ctx, portal, intent, m.Client.UserLogin, m.Client.Session, m.Data, m.ThreadRootID), nil
}

func (m *DiscordMessage) GetID() networkid.MessageID {
	return discordid.MakeMessageID(m.Data.ID)
}

func (m *DiscordMessage) GetSender() bridgev2.EventSender {
	if m.Data.Author == nil {
		// Message deletions don't have a sender associated with them.
		return bridgev2.EventSender{}
	}

	return m.Client.makeEventSender(m.Data.Author)
}

func (d *DiscordClient) wrapDiscordMessage(ctx context.Context, msg *discordgo.Message, typ bridgev2.RemoteEventType) DiscordMessage {
	if msg == nil {
		msg = &discordgo.Message{}
	}
	portalChannelID, threadRootID, err := d.getThreadPortalInfo(ctx, msg.ChannelID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("channel_id", msg.ChannelID).
			Str("message_id", msg.ID).
			Msg("Failed to resolve thread mapping for message")
		portalChannelID = msg.ChannelID
		threadRootID = nil
	}
	return DiscordMessage{
		DiscordEventMeta: &DiscordEventMeta{
			Type: typ,
			PortalKey: networkid.PortalKey{
				ID:       discordid.MakeChannelPortalIDWithID(portalChannelID),
				Receiver: d.UserLogin.ID,
			},
		},
		Data:         msg,
		Client:       d,
		ThreadRootID: threadRootID,
	}
}

type DiscordReaction struct {
	*DiscordEventMeta
	Reaction *discordgo.MessageReaction
	Client   *DiscordClient

	Emoji   string
	EmojiID networkid.EmojiID
	Extra   map[string]any
}

func (r *DiscordReaction) GetSender() bridgev2.EventSender {
	return r.Client.makeEventSenderWithID(r.Reaction.UserID)
}

func (r *DiscordReaction) GetTargetMessage() networkid.MessageID {
	return discordid.MakeMessageID(r.Reaction.MessageID)
}

func (r *DiscordReaction) GetRemovedEmojiID() networkid.EmojiID {
	return r.EmojiID
}

var (
	_ bridgev2.RemoteReaction                 = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionRemove           = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionWithExtraContent = (*DiscordReaction)(nil)
)

func (r *DiscordReaction) GetReactionEmoji() (string, networkid.EmojiID) {
	return r.Emoji, r.EmojiID
}

func (r *DiscordReaction) GetReactionExtraContent() map[string]any {
	return r.Extra
}

func (d *DiscordClient) wrapDiscordReaction(ctx context.Context, reaction *discordgo.MessageReaction, beingAdded bool) (*DiscordReaction, error) {
	if reaction == nil {
		return nil, nil
	}
	evtType := bridgev2.RemoteEventReaction
	if !beingAdded {
		evtType = bridgev2.RemoteEventReactionRemove
	}

	portalChannelID, _, resolveErr := d.getThreadPortalInfo(ctx, reaction.ChannelID)
	if resolveErr != nil {
		zerolog.Ctx(ctx).Err(resolveErr).
			Str("reaction_channel_id", reaction.ChannelID).
			Str("reaction_message_id", reaction.MessageID).
			Msg("Failed to resolve thread mapping for reaction")
		portalChannelID = reaction.ChannelID
	}

	var matrixEmoji string
	var emojiID string
	var extra map[string]any

	if reaction.Emoji.ID != "" {
		// A custom emoji.
		emojiID = fmt.Sprintf("%s:%s", reaction.Emoji.Name, reaction.Emoji.ID)
		shortcode := fmt.Sprintf(":%s:", reaction.Emoji.Name)

		extra = map[string]any{
			"fi.mau.discord.reaction": map[string]any{
				"id":   reaction.Emoji.ID,
				"name": reaction.Emoji.Name,
				// "mxc" is added later if it's `beingAdded`.
			},
			"com.beeper.reaction.shortcode": shortcode,
		}

		if beingAdded {
			reactionMXC, err := d.connector.GetCustomEmojiMXC(
				ctx,
				reaction.Emoji.ID,
				reaction.Emoji.Name,
				reaction.Emoji.Animated,
			)

			if err != nil || reactionMXC == "" {
				zerolog.Ctx(ctx).Err(err).
					Str("emoji_id", reaction.Emoji.ID).
					Str("emoji_name", reaction.Emoji.Name).
					Msg("Failed to get Matrix MXC for custom emoji reaction being added")
				return nil, err
			}

			extra["fi.mau.discord.reaction"].(map[string]any)["mxc"] = reactionMXC

			if d.connector.Config.CustomEmojiReactionsEnabled() {
				matrixEmoji = string(reactionMXC)
			} else {
				matrixEmoji = shortcode
			}
		}
	} else {
		// A Unicode emoji.
		emojiID = reaction.Emoji.Name
		matrixEmoji = variationselector.Add(reaction.Emoji.Name)
	}

	return &DiscordReaction{
		DiscordEventMeta: &DiscordEventMeta{
			Type: evtType,
			PortalKey: networkid.PortalKey{
				ID:       discordid.MakeChannelPortalIDWithID(portalChannelID),
				Receiver: d.UserLogin.ID,
			},
		},
		Reaction: reaction,
		Client:   d,
		Emoji:    matrixEmoji,
		EmojiID:  discordid.MakeEmojiID(emojiID),
		Extra:    extra,
	}, nil
}

func (d *DiscordClient) handleDiscordTyping(ctx context.Context, typing *discordgo.TypingStart) {
	if typing.UserID == d.Session.State.User.ID {
		return
	}

	log := zerolog.Ctx(ctx)

	portalChannelID, _, err := d.getThreadPortalInfo(ctx, typing.ChannelID)
	if err != nil {
		log.Err(err).Str("channel_id", typing.ChannelID).Msg("Failed to resolve thread mapping for typing event")
		portalChannelID = typing.ChannelID
	}

	portalKey := networkid.PortalKey{
		ID:       discordid.MakeChannelPortalIDWithID(portalChannelID),
		Receiver: d.UserLogin.ID,
	}
	portal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		log.Err(err).Msg("Failed to query for existing portal")
		return
	}
	if portal == nil || portal.MXID == "" {
		return
	}

	// Make sure we have this user's info in case we haven't seen them at all yet.
	_ = d.userCache.Resolve(ctx, typing.UserID)

	d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: portalKey,
			Sender:    d.makeEventSenderWithID(typing.UserID),
		},
		Timeout: 12 * time.Second,
		Type:    bridgev2.TypingTypeText,
	})
}

func (d *DiscordClient) handleChannelUpdate(ctx context.Context, upd *discordgo.ChannelUpdate) error {
	if upd.BeforeUpdate == nil {
		// Channel doesn't exist in the discordgo's state; don't bother bridging.
		return nil
	}

	log := zerolog.Ctx(ctx).With().Str("action", "handle channel update").Logger()
	ctx = log.WithContext(ctx)

	portalKey := networkid.PortalKey{
		ID:       discordid.MakeChannelPortalIDWithID(upd.ID),
		Receiver: d.UserLogin.ID,
	}
	portal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		return fmt.Errorf("failed to look up existing channel: %w", err)
	}
	if portal == nil {
		// Don't bridge updates for channels we haven't actually bridged.
		return nil
	}

	ts := time.Now()
	// Re-use main GetChatInfo logic to avoid drift. The rest of this function
	// is mostly removing what didn't change.
	patch, err := d.GetChatInfo(ctx, portal)
	if err != nil {
		return fmt.Errorf("failed to recompute chat info: %w", err)
	}

	patch.Type = nil
	patch.CanBackfill = false
	patch.ExtraUpdates = nil

	old := upd.BeforeUpdate
	// People leaving or joining a group DM isn't expressed via CHANNEL_UPDATE.
	patch.Members = nil
	if upd.Name == old.Name {
		patch.Name = nil
	}
	if upd.Topic == old.Topic {
		patch.Topic = nil
	}
	if upd.Icon == old.Icon {
		patch.Avatar = nil
	}
	if upd.ParentID == old.ParentID {
		patch.ParentID = nil
	}

	d.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Timestamp: ts,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: patch,
		},
	})

	return nil
}

func (d *DiscordClient) handleThreadUpdate(ctx context.Context, thread *discordgo.Channel) error {
	if thread == nil || !isThread(thread) {
		return nil
	}
	return d.upsertThreadInfoFromChannel(ctx, thread)
}

func (d *DiscordClient) handleThreadDelete(ctx context.Context, thread *discordgo.Channel) error {
	if thread == nil || thread.ID == "" {
		return nil
	}
	return d.connector.DB.Thread.DeleteByThreadChannelID(ctx, string(d.UserLogin.ID), thread.ID)
}

func (d *DiscordClient) handleDiscordEvent(rawEvt any) {
	defer func() {
		err := recover()
		if err != nil {
			d.UserLogin.Log.Error().
				Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
				Any(zerolog.ErrorFieldName, err).
				Msg("Panic in Discord event handler")
		}
	}()

	log := d.UserLogin.Log.With().Str("action", "handle discord event").
		Type("event_type", rawEvt).
		Logger()
	ctx := log.WithContext(d.UserLogin.Bridge.BackgroundCtx)

	// NOTE: discordgo seemingly dispatches both the proper unmarshalled type
	// (e.g. `*discordgo.TypingStart`) _as well as_ a "raw" *discordgo.Event
	// (e.g. `*discordgo.Event` with `Type` of `TYPING_START`) for every gateway
	// event
	switch evt := rawEvt.(type) {
	case *discordgo.Ready:
		log.Info().Msg("Received READY dispatch from discordgo")
		d.userCache.UpdateWithReady(evt)
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateConnected,
		})
	case *discordgo.TypingStart:
		d.handleDiscordTyping(ctx, evt)
	case *discordgo.Resumed:
		log.Info().Msg("Received RESUMED dispatch from discordgo")
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateConnected,
		})
	case *discordgo.GuildCreate:
		if evt.Unavailable {
			break
		}
		if err := d.syncGuildRoles(ctx, evt.ID, evt.Roles); err != nil {
			log.Err(err).Str("guild_id", evt.ID).Msg("Failed to sync guild roles from guild create event")
		}
	case *discordgo.GuildUpdate:
		if err := d.syncGuildRoles(ctx, evt.ID, evt.Roles); err != nil {
			log.Err(err).Str("guild_id", evt.ID).Msg("Failed to sync guild roles from guild update event")
		}
	case *discordgo.GuildRoleCreate:
		roleID := ""
		if evt.Role != nil {
			roleID = evt.Role.ID
		}
		if err := d.upsertGuildRole(ctx, evt.GuildID, evt.Role); err != nil {
			log.Err(err).Str("guild_id", evt.GuildID).Str("role_id", roleID).Msg("Failed to store role create event")
		}
	case *discordgo.GuildRoleUpdate:
		roleID := ""
		if evt.Role != nil {
			roleID = evt.Role.ID
		}
		if err := d.upsertGuildRole(ctx, evt.GuildID, evt.Role); err != nil {
			log.Err(err).Str("guild_id", evt.GuildID).Str("role_id", roleID).Msg("Failed to store role update event")
		}
	case *discordgo.GuildRoleDelete:
		if err := d.connector.DB.Role.DeleteByID(ctx, evt.GuildID, evt.RoleID); err != nil {
			log.Err(err).Str("guild_id", evt.GuildID).Str("role_id", evt.RoleID).Msg("Failed to delete role from database")
		}
	case *discordgo.ChannelUpdate:
		err := d.handleChannelUpdate(ctx, evt)
		if err != nil {
			log.Err(err).Msg("Failed to handle channel update")
		}
	case *discordgo.ThreadCreate:
		err := d.handleThreadUpdate(ctx, evt.Channel)
		if err != nil {
			log.Err(err).Str("thread_id", evt.ID).Msg("Failed to handle thread create event")
		}
	case *discordgo.ThreadUpdate:
		err := d.handleThreadUpdate(ctx, evt.Channel)
		if err != nil {
			log.Err(err).Str("thread_id", evt.ID).Msg("Failed to handle thread update event")
		}
	case *discordgo.ThreadDelete:
		err := d.handleThreadDelete(ctx, evt.Channel)
		if err != nil {
			log.Err(err).Str("thread_id", evt.ID).Msg("Failed to handle thread delete event")
		}
	case *discordgo.ThreadListSync:
		for _, thread := range evt.Threads {
			err := d.handleThreadUpdate(ctx, thread)
			if err != nil {
				log.Err(err).Str("thread_id", thread.ID).Msg("Failed to handle thread in thread list sync event")
			}
		}
	case *discordgo.MessageCreate:
		if evt.Author == nil {
			log.Trace().Int("message_type", int(evt.Message.Type)).
				Str("guild_id", evt.GuildID).
				Str("message_id", evt.ID).
				Str("channel_id", evt.ChannelID).
				Msg("Dropping message that lacks an author")
			return
		}
		if err := d.upsertThreadInfoFromMessage(ctx, evt.Message); err != nil {
			log.Err(err).Str("message_id", evt.ID).Msg("Failed to persist thread info from message create")
		}
		d.userCache.UpdateWithMessage(evt.Message)
		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, bridgev2.RemoteEventMessage)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.MessageUpdate:
		if err := d.upsertThreadInfoFromMessage(ctx, evt.Message); err != nil {
			log.Err(err).Str("message_id", evt.ID).Msg("Failed to persist thread info from message update")
		}
		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, bridgev2.RemoteEventEdit)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.UserUpdate:
		d.userCache.UpdateWithUserUpdate(evt)
	case *discordgo.MessageDelete:
		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, bridgev2.RemoteEventMessageRemove)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	// TODO *discordgo.MessageDeleteBulk
	case *discordgo.MessageReactionAdd:
		wrappedEvt, err := d.wrapDiscordReaction(ctx, evt.MessageReaction, true)
		if wrappedEvt != nil && err == nil {
			d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, wrappedEvt)
		}
	case *discordgo.MessageReactionRemove:
		wrappedEvt, err := d.wrapDiscordReaction(ctx, evt.MessageReaction, false)
		if wrappedEvt != nil && err == nil {
			d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, wrappedEvt)
		}
	// TODO case *discordgo.MessageReactionRemoveAll:
	// TODO case *discordgo.MessageReactionRemoveEmoji: (needs impl. in discordgo)
	case *discordgo.PresenceUpdate:
		return
	case *discordgo.GuildDelete:
		if evt.Unavailable {
			log.Warn().Str("guild_id", evt.ID).Msg("Guild became unavailable")
			// For now, leave the portals alone if the guild only went away due to an outage.
			return
		}
		if err := d.connector.DB.Role.DeleteByGuildID(ctx, evt.ID); err != nil {
			log.Err(err).Str("guild_id", evt.ID).Msg("Failed to delete guild roles from database")
		}
		d.deleteGuildPortalSpace(ctx, evt.ID)
	}
}
