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
	"go.mau.fi/util/exmaps"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/util/variationselector"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

type DiscordEventMeta struct {
	Type       bridgev2.RemoteEventType
	LogContext func(c zerolog.Context) zerolog.Context
	route      router.Route
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
	return em.route.PortalKey
}

func (em *DiscordEventMeta) PortalReceiverIsUncertain() bool {
	return em.route.Uncertain
}

type DiscordMessage struct {
	*DiscordEventMeta
	Data         *discordgo.Message
	Client       *DiscordClient
	ThreadRootID *networkid.MessageID
}

func (m *DiscordMessage) ShouldCreatePortal() bool {
	// Do not create a portal merely to bridge a message deletion or edit.
	return m.Type == bridgev2.RemoteEventMessage
}

func (m *DiscordMessage) ConvertEdit(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	existingParts []*database.Message,
) (*bridgev2.ConvertedEdit, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "convert discord edit").Logger()
	ctx = log.WithContext(ctx)

	// FIXME(skip): This will always reupload attachments, super wasteful.
	newlyConverted := m.Client.connector.MsgConv.ToMatrix(
		ctx,
		portal,
		intent,
		m.Client.UserLogin,
		m.Client.Session,
		m.Data,
		m.ThreadRootID,
	)

	// Detect the legacy scheme of naively assigning incrementing part IDs
	// without using stable identifiers, so we don't cause churn on previously
	// bridged messages that are edited.
	if isLegacyNumericParts(existingParts) {
		return legacyConvertEdit(ctx, newlyConverted, existingParts)
	}

	beforePartsByID := make(map[networkid.PartID]*database.Message, len(existingParts))
	for _, part := range existingParts {
		beforePartsByID[part.PartID] = part
	}
	afterPartIDs := make(exmaps.Set[networkid.PartID], len(newlyConverted.Parts))
	for _, part := range newlyConverted.Parts {
		afterPartIDs.Add(part.ID)
	}

	edit := &bridgev2.ConvertedEdit{}

	// If a part ID is no longer present after converting the edited version,
	// then it was deleted.
	for _, part := range existingParts {
		if !afterPartIDs.Has(part.PartID) {
			edit.DeletedParts = append(edit.DeletedParts, part)
		}
	}

	var addedParts []*bridgev2.ConvertedMessagePart
	for _, part := range newlyConverted.Parts {
		dbPart, ok := beforePartsByID[part.ID]
		if !ok {
			// Part ID is new, so it's being added.
			addedParts = append(addedParts, part)
			continue
		}

		// TODO(skip): Stash the edited timestamp of messages so we can
		// actually discern between link previews/embeds and the message text
		// actually being edited. As is, this will always replace the message
		// body.
		if part.ID == "" {
			edit.ModifiedParts = append(edit.ModifiedParts, part.ToEditPart(dbPart))
		}
	}
	if len(addedParts) > 0 {
		edit.AddedParts = &bridgev2.ConvertedMessage{
			Parts:      addedParts,
			ThreadRoot: newlyConverted.ThreadRoot,
		}
	}

	return edit, nil
}

// isLegacyNumericParts reports whether the existing parts on a message were
// assigned the old incrementing numeric part IDs.
func isLegacyNumericParts(existingParts []*database.Message) bool {
	if len(existingParts) == 0 {
		return false
	}

	for _, part := range existingParts {
		partIDString := string(part.PartID)
		partID, err := strconv.Atoi(partIDString)
		if err != nil {
			return false
		}
		if partID < 0 || partID >= len(existingParts) {
			// outside of range
			return false
		}
		if strconv.Itoa(partID) != partIDString {
			// round-trip
			return false
		}
	}

	return true
}

// legacyConvertEdit performs legacy message part edit handling, appropriate
// for messages that were bridged before stable part IDs were assigned.
func legacyConvertEdit(ctx context.Context, converted *bridgev2.ConvertedMessage, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	log := zerolog.Ctx(ctx)
	slices.SortStableFunc(existing, func(a *database.Message, b *database.Message) int {
		ai, _ := strconv.Atoi(string(a.PartID))
		bi, _ := strconv.Atoi(string(b.PartID))
		return ai - bi
	})

	if len(converted.Parts) != len(existing) {
		log.Warn().
			Int("n_parts_existing", len(existing)).
			Int("n_parts_after_edit", len(converted.Parts)).
			Msg("Ignoring legacy message edit that changed number of parts")
		return nil, bridgev2.ErrIgnoringRemoteEvent
	}

	parts := make([]*bridgev2.ConvertedEditPart, 0, len(existing))
	for pi, part := range converted.Parts {
		parts = append(parts, part.ToEditPart(existing[pi]))
	}

	return &bridgev2.ConvertedEdit{
		ModifiedParts: parts,
	}, nil
}

var (
	_ bridgev2.RemoteMessage                          = (*DiscordMessage)(nil)
	_ bridgev2.RemoteMessageWithTransactionID         = (*DiscordMessage)(nil)
	_ bridgev2.RemoteMessageRemove                    = (*DiscordMessage)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal         = (*DiscordMessage)(nil)
	_ bridgev2.RemoteEventWithUncertainPortalReceiver = (*DiscordMessage)(nil)
	_ bridgev2.RemoteEdit                             = (*DiscordMessage)(nil)
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

func (d *DiscordClient) wrapDiscordMessage(ctx context.Context, msg *discordgo.Message, route *router.Route, typ bridgev2.RemoteEventType) DiscordMessage {
	if msg == nil {
		msg = &discordgo.Message{}
	}

	return DiscordMessage{
		DiscordEventMeta: &DiscordEventMeta{
			Type:  typ,
			route: *route,
		},
		Data:         msg,
		Client:       d,
		ThreadRootID: route.FromThreadRootMessageID(),
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
	_ bridgev2.RemoteReaction                         = (*DiscordReaction)(nil)
	_ bridgev2.RemoteEventWithUncertainPortalReceiver = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionRemove                   = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionWithExtraContent         = (*DiscordReaction)(nil)
)

func (r *DiscordReaction) GetReactionEmoji() (string, networkid.EmojiID) {
	return r.Emoji, r.EmojiID
}

func (r *DiscordReaction) GetReactionExtraContent() map[string]any {
	return r.Extra
}

func (d *DiscordClient) wrapDiscordReaction(ctx context.Context, reaction *discordgo.MessageReaction, route *router.Route, beingAdded bool) (*DiscordReaction, error) {
	if reaction == nil {
		return nil, nil
	}
	evtType := bridgev2.RemoteEventReaction
	if !beingAdded {
		evtType = bridgev2.RemoteEventReactionRemove
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
			Type:  evtType,
			route: *route,
		},
		Reaction: reaction,
		Client:   d,
		Emoji:    matrixEmoji,
		EmojiID:  discordid.MakeEmojiID(emojiID),
		Extra:    extra,
	}, nil
}

func (d *DiscordClient) handleDiscordTyping(ctx context.Context, typing *discordgo.TypingStart, route *router.Route) {
	if typing.UserID == d.Session.State.User.ID {
		return
	}

	log := zerolog.Ctx(ctx).With().
		Str("typing_channel_id", typing.ChannelID).
		Str("typing_user_id", typing.UserID).
		Str("typing_guild_id", typing.GuildID).
		Logger()
	ctx = log.WithContext(ctx)

	// Make sure we have this user's info in case we haven't seen them at all yet.
	_ = d.userCache.Resolve(ctx, typing.UserID)

	d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:              bridgev2.RemoteEventTyping,
			PortalKey:         route.PortalKey,
			Sender:            d.makeEventSenderWithID(typing.UserID),
			UncertainReceiver: route.Uncertain,
		},
		Timeout: 12 * time.Second,
		Type:    bridgev2.TypingTypeText,
	})
}

func (d *DiscordClient) handleChannelCreate(ctx context.Context, ch *discordgo.ChannelCreate) error {
	log := zerolog.Ctx(ctx).With().
		Str("guild_id", ch.GuildID).
		Str("channel_id", ch.ID).
		Str("channel_parent_id", ch.ParentID).
		Str("channel_type", readableChannelType(ch.Type)).
		Str("action", "handle channel create").Logger()
	ctx = log.WithContext(ctx)

	if ch.GuildID == "" {
		log.Debug().Msg("Private channel was created, creating portal")
	} else {
		if !d.shouldBridgeChannel(ctx, ch.Channel) {
			log.Debug().Msg("Ignoring creation of guild channel that should not be bridged")
			return nil
		}

		log.Debug().Msg("Guild channel was created")

		// If the newly created channel is under a category, ensure that the
		// corresponding parent space exists first, so m.bridge is correct.
		if ch.ParentID != "" {
			parentCh := d.channelWithID(ctx, ch.ParentID)
			if parentCh == nil {
				log.Error().Msg("Newly created guild channel has a parent channel, but it's not present in cache; dropping!")
				return nil
			}
			log.Debug().Msg("Ensuring parent space for the newly created channel")
			err := d.ensurePortal(ctx, d.portalKeyForChannel(parentCh), nil)
			if err != nil {
				log.Err(err).Msg("Failed to ensure category space, dropping!")
				return nil
			}
		}
	}

	// This creates the portal.
	d.queueChannelResync(ctx, ch.Channel)

	return nil
}

func (d *DiscordClient) handleChannelUpdate(ctx context.Context, upd *discordgo.ChannelUpdate) error {
	if upd.BeforeUpdate == nil {
		// Channel doesn't exist in the discordgo's state; don't bother bridging.
		return nil
	}

	log := zerolog.Ctx(ctx).With().Str("action", "handle channel update").Logger()
	ctx = log.WithContext(ctx)

	portalKey := d.portalKeyForChannel(upd.Channel)
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

// handleChannelDelete handles a channel being deleted. This can be a guild
// channel getting "actually" deleted or a private channel getting "closed".
func (d *DiscordClient) handleChannelDelete(ctx context.Context, evt *discordgo.ChannelDelete) error {
	portalKey := d.portalKeyForChannel(evt.Channel)
	log := zerolog.Ctx(ctx).With().
		Str("channel_id", evt.ID).
		Str("guild_id", evt.GuildID).
		Stringer("deleted_channel_portal_key", portalKey).Logger()

	log.Debug().Msg("Handling channel deletion")
	d.queueChatDelete(portalKey, evt.Channel.GuildID)

	return nil
}

func (d *DiscordClient) queueChatDelete(portalKey networkid.PortalKey, deletedChannelGuildID string) {
	ts := time.Now()

	onlyForMe := true
	if !d.connector.Bridge.Config.SplitPortals && deletedChannelGuildID != "" {
		// When split portals are disabled and a guild channel was deleted,
		// then it should be deleted for everyone.
		onlyForMe = false
	}

	d.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: portalKey,
			Timestamp: ts,
		},
		OnlyForMe: onlyForMe,
		// Do not pass Children: true as deleting a guild channel category
		// merely detaches the parent_id from all child channels.
		// CHANNEL_UPDATE events will be dispatched for all child channels,
		// which should reparent them.
	})
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

func (d *DiscordClient) queueIndividualMembershipChange(
	ctx context.Context,
	portalKey networkid.PortalKey,
	user *discordgo.User,
	membership event.Membership,
	ts time.Time,
) {
	log := zerolog.Ctx(ctx)

	userID := discordid.MakeUserID(user.ID)
	info := d.getUserInfo(ctx, user)

	log.Debug().
		Stringer("portal_key", portalKey).
		Str("moving_user_id", user.ID).
		Str("membership", string(membership)).
		Msg("Queueing chat info change in response to membership change")

	d.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Timestamp: ts,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				MemberMap: bridgev2.ChatMemberMap{
					userID: bridgev2.ChatMember{
						// TODO: Can't effectively send MemberSender here to
						// attribute e.g. someone getting kicked from a group
						// DM because that information isn't in the gateway
						// payload. Might need to wait for the corresponding
						// system message.
						EventSender: d.makeEventSender(user),
						Membership:  membership,
						UserInfo:    info,
					},
				},
			},
		},
	})
}

func (d *DiscordClient) handleRecipientAdd(ctx context.Context, evt *discordgo.ChannelRecipientAdd, route *router.Route) error {
	d.queueIndividualMembershipChange(ctx, route.PortalKey, evt.User, event.MembershipJoin, time.Now())
	return nil
}

func (d *DiscordClient) handleRecipientRemove(ctx context.Context, evt *discordgo.ChannelRecipientRemove, route *router.Route) error {
	d.queueIndividualMembershipChange(ctx, route.PortalKey, evt.User, event.MembershipLeave, time.Now())
	return nil
}

func (d *DiscordClient) handleGuildMemberJoinMessage(ctx context.Context, msg *discordgo.Message, route *router.Route) {
	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	d.queueIndividualMembershipChange(ctx, route.PortalKey, msg.Author, event.MembershipJoin, ts)
}

func (d *DiscordClient) handlePresenceUpdate(ctx context.Context, evt *discordgo.PresenceUpdate) {
	// NOTE: This can potentially be a _very_ hot code path for users who can
	// see a lot of other users (e.g. member of a large guild, member of many
	// guilds).

	user := evt.User
	if user == nil || user.ID == "" {
		return
	}

	// We only care about profile updates, so bail if it's just the
	// status/activity that changed.
	if user.Username == "" && user.GlobalName == "" && user.Discriminator == "" && user.Avatar == "" {
		return
	}

	log := zerolog.Ctx(ctx).With().
		Str("presence_update_guild_id", evt.GuildID).
		Str("presence_update_user_id", user.ID).
		Logger()
	ctx = log.WithContext(ctx)

	// Incorporate the user delta into the cache.
	merged := d.userCache.MergePartialUser(user)
	if merged == nil {
		// The user wasn't cached in the first place, so this presence update
		// is likely irrelevant to us.
		log.Trace().Msg("Ignoring presence update for uncached user")
		return
	}

	// Check if a ghost actually exists for this user; don't eagerly
	// materialize ghosts just because we happen to be subscribed to their
	// presence.
	ghost, err := d.connector.Bridge.GetExistingGhostByID(ctx, discordid.MakeUserID(user.ID))
	if err != nil {
		log.Err(err).Msg("Failed to look up existing ghost for presence update")
		return
	}
	if ghost == nil {
		return
	}

	log.Debug().Msg("Dispatching ghost info update after profile change")
	ghost.UpdateInfo(ctx, d.getUserInfo(ctx, merged))
}

func (d *DiscordClient) handleMessageAck(ctx context.Context, ack *discordgo.MessageAck, bridged bool, route *router.Route) {
	d.readStatesLock.Lock()
	zerolog.Ctx(ctx).Trace().
		Str("channel_id", ack.ChannelID).
		Str("message_id", ack.MessageID).
		Msg("Updating state with MESSAGE_ACK")

	// TODO: mention_count can appear in MESSAGE_ACK payloads. Update it if it's
	// present and not `null`. This needs discordgo changes. (There's even more
	// missing fields than this.)
	d.readStates[ack.ChannelID] = &discordgo.ReadState{
		ID:            ack.ChannelID,
		LastMessageID: discordgo.StringOrInt(ack.MessageID),
	}
	d.readStatesLock.Unlock()

	if bridged {
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type:              bridgev2.RemoteEventReadReceipt,
				PortalKey:         route.PortalKey,
				Sender:            d.selfEventSender(),
				UncertainReceiver: route.Uncertain,
			},
			LastTarget: discordid.MakeMessageID(ack.MessageID),
		})
	}
}

// channelIsBridged uses routing logic to check whether a portal (with an
// existing room) exists for a given Discord channel ID.
func (d *DiscordClient) channelIsBridged(ctx context.Context, channelID string) (bool, *router.Route) {
	log := zerolog.Ctx(ctx)

	route, err := d.Route(ctx, channelID)
	if err != nil {
		log.Err(err).Msg("Failed to route channel when determining channel bridgedness")
		return false, nil
	}
	existingPortal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, route.PortalKey)
	if err != nil {
		log.Err(err).Msg("Failed to look up existing portal when determining channel bridgedness")
		return false, route
	}
	return existingPortal != nil && existingPortal.MXID != "", route
}

func (d *DiscordClient) isOwnRelayWebhookMessage(ctx context.Context, msg *discordgo.Message, route *router.Route) bool {
	if msg == nil || route == nil {
		return false
	}
	portal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, route.PortalKey)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to look up portal when checking relay webhook message")
		return false
	}
	if portal == nil {
		return false
	}
	meta := portal.Metadata.(*discordid.PortalMetadata)
	return isRelayWebhookDiscordMessage(msg, meta.RelayWebhookID)
}

func isRelayWebhookDiscordMessage(msg *discordgo.Message, relayWebhookID string) bool {
	if msg == nil || relayWebhookID == "" {
		return false
	}
	if msg.WebhookID == relayWebhookID {
		return true
	}
	return msg.WebhookID != "" && msg.Author != nil && msg.Author.ID == relayWebhookID
}

func (d *DiscordClient) handleUserGuildSettingsUpdate(ctx context.Context, evt *discordgo.UserGuildSettingsUpdate) {
	log := zerolog.Ctx(ctx)
	log.Debug().Msg("Handling user guild settings update")
	d.applySingleGuildSettings(evt.UserGuildSettings)
}

func messageCtx(ctx context.Context, msg *discordgo.Message) (context.Context, *zerolog.Logger) {
	if msg == nil {
		return ctx, zerolog.Ctx(ctx)
	}

	wipLog := zerolog.Ctx(ctx).With().
		Str("guild_id", msg.GuildID).
		Str("channel_id", msg.ChannelID).
		Str("message_id", msg.ID)
	if msg.Author != nil {
		wipLog = wipLog.Str("author_id", msg.Author.ID).
			Bool("author_bot", msg.Author.Bot)
	}
	if msg.WebhookID != "" {
		wipLog = wipLog.Str("webhook_id", msg.WebhookID)
	}
	log := wipLog.Logger()

	return log.WithContext(ctx), &log
}

func (d *DiscordClient) handleDiscordStateEvent(rawEvt any) {
	ctx := d.UserLogin.Bridge.BackgroundCtx
	log := zerolog.Ctx(ctx)

	switch evt := rawEvt.(type) {
	case *discordgo.ReadySupplemental:
		log.Info().
			Int("n_lazy_private_channels", len(evt.LazyPrivateChannels)).
			Msg("Received supplemental READY")
	case *discordgo.Ready:
		wasSeen := d.seenReady.Swap(true)

		d.applyReadyPayload(ctx, evt)
		d.pokeVitals(ctx)

		// A READY after the first one means the gateway handed us a fresh
		// session instead of resuming (our resume was refused or the session
		// was invalidated), so Discord didn't replay the events we missed
		// while offline.
		if wasSeen {
			log.Info().Msg("Reconnected without resuming, re-syncing chats and spaces")
			d.beginResyncingChatsAndSpaces(ctx)
		}
	case *discordgo.MessageCreate:
		if evt.Author == nil {
			return
		}

		urgent := evt.Flags&discordgo.MessageFlagsUrgent != 0
		system := evt.Author.System
		if !urgent && !system {
			return
		}

		var le *zerolog.Event
		if urgent {
			le = log.Warn()
		} else {
			le = log.Info()
		}
		le.Bool("message_urgent", urgent).
			Bool("message_system", system).
			Msg("Received system message")

		if urgent {
			d.refreshSafetyHub(ctx)
		}

		// Discord's first-party client does this. A USER_UPDATE gateway event
		// is not sent out when this bit becomes true (due to a new system
		// message), but it *does* get sent out when another client does PATCH
		// /users/@me with {flags:…}.
		d.Session.State.Lock()
		d.Session.State.User.Flags |= int(discordgo.UserFlagHasUnreadUrgentMessages)
		d.Session.State.Unlock()

		d.pokeVitals(ctx)
	case *discordgo.UserRequiredActionUpdate:
		if evt.RequiredAction == "" {
			log.Info().Msg("Required action was performed")
		} else {
			log.Error().Str("required_action", string(evt.RequiredAction)).
				Msg("Required action was updated")
		}
		d.pokeVitals(ctx)
	case *discordgo.RelationshipAdd:
		d.upsertRelationship(evt.Relationship)
	case *discordgo.RelationshipUpdate:
		d.upsertRelationship(evt.Relationship)
	case *discordgo.RelationshipRemove:
		d.removeRelationship(evt.ID)
	}
}

func (d *DiscordClient) handleRelationshipNickChange(ctx context.Context, userID, nickname string) {
	ch := d.dmChannelForUserID(userID)
	if ch == nil {
		return
	}

	portalKey := d.portalKeyForChannel(ch)
	portal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to look up DM portal for relationship nick change")
		return
	}
	if portal == nil || portal.MXID == "" {
		return
	}

	var name *string
	if nickname != "" {
		name = &nickname
	} else {
		name = bridgev2.DefaultChatName
	}

	d.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Name: name,
			},
		},
	})
}

func (d *DiscordClient) handleDiscordEvent(rawEvt any) {
	defer func() {
		err := recover()
		if err == nil {
			return
		}

		d.UserLogin.Log.Error().
			Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
			Any(zerolog.ErrorFieldName, err).
			Msg("Panic in Discord event handler")

		props := d.baseAnalyticsProps(d.UserLogin.Bridge.BackgroundCtx)
		props["eventType"] = fmt.Sprintf("%T", rawEvt)
		props["error"] = fmt.Sprint(err)

		d.UserLogin.TrackAnalytics("Discord event handler panic", props)
	}()

	log := d.UserLogin.Log.With().Str("action", "handle discord event").
		Type("event_type", rawEvt).
		Logger()
	ctx := log.WithContext(d.UserLogin.Bridge.BackgroundCtx)

	// NOTE: discordgo seemingly dispatches both the proper unmarshalled type
	// (e.g. `*discordgo.TypingStart`) _as well as_ a "raw" *discordgo.Event
	// (e.g. `*discordgo.Event` with `Type` of `TYPING_START`) for every gateway
	// event.

	// NOTE: We explicitly return early from paths where we would otherwise
	// QueueRemoteEvent for a portal that hasn't been bridged by the user yet.
	// (Specifically, we check for an extant portal with an associated room.)
	// This avoids the eager creation of stub portals that have bogus metadata
	// (e.g. GuildID == "" despite being a guild channel). This is because you
	// can't specify metadata upfront when a portal is implicitly created. We
	// might want to rely on our metadata always being "correct" in the future.
	//
	// This also helps avoid excessive "Dropping event as portal doesn't exist"
	// logs from Mautrix. You receive events for every guild you're in, so this
	// can become noisy fast.

	switch evt := rawEvt.(type) {
	case *discordgo.Ready:
		log.Info().
			Int("n_dms", len(evt.PrivateChannels)).
			Int("n_guilds", len(evt.Guilds)).
			Int("n_merged_members", len(evt.MergedMembers)).
			Int("n_relationships", len(evt.Relationships)).
			Int("n_users", len(evt.Users)).
			Msg("Received READY dispatch from discordgo")

		// Catch up on profile changes that might've occurred while we were
		// offline.
		d.syncRemoteProfile(ctx)
		go d.resyncGhostsFromReady(ctx, evt)
		d.refreshSafetyHub(ctx)
		d.pokeVitals(ctx)
		d.sendCurrentState(ctx) // (pokeVitals already enqueued a new bridge state but let's be explicit about it here.)
	case *discordgo.Resumed:
		// (All missed gateway events have been replayed, and all subsequent
		// events will be new.)
		log.Info().Msg("Received RESUMED dispatch from discordgo")
		d.refreshSafetyHub(ctx)
		d.sendCurrentState(ctx)
	case *discordgo.InvalidAuth:
		log.Warn().Msg("Got logged out of Discord due to invalid token")
		d.tokenInvalidated(ctx, "while connected")
	case *discordgo.TypingStart:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		d.handleDiscordTyping(ctx, evt, route)
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
	case *discordgo.ChannelCreate:
		if err := d.handleChannelCreate(ctx, evt); err != nil {
			log.Err(err).Msg("Failed to handle channel create")
		}
	case *discordgo.ChannelUpdate:
		bridged, _ := d.channelIsBridged(ctx, evt.ID)
		if !bridged {
			return
		}
		err := d.handleChannelUpdate(ctx, evt)
		if err != nil {
			log.Err(err).Msg("Failed to handle channel update")
		}
	case *discordgo.ChannelDelete:
		// The route computed by channelIsBridged will always be uncertain
		// because the channel has already disappeared from discordgo's state.
		bridged, _ := d.channelIsBridged(ctx, evt.ID)
		if !bridged {
			return
		}
		if err := d.handleChannelDelete(ctx, evt); err != nil {
			log.Err(err).Msg("Failed to handle channel delete")
		}
	case *discordgo.ChannelRecipientAdd:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		if err := d.handleRecipientAdd(ctx, evt, route); err != nil {
			log.Err(err).Msg("Failed to handle channel recipient add")
		}
	case *discordgo.ChannelRecipientRemove:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		if err := d.handleRecipientRemove(ctx, evt, route); err != nil {
			log.Err(err).Msg("Failed to handle channel recipient remove")
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
		ctx, log := messageCtx(ctx, evt.Message)
		inBridgedChannel, route := d.channelIsBridged(ctx, evt.ChannelID)
		isDM := route != nil && route.FromChannel != nil && channelIsPrivate(route.FromChannel)
		if !inBridgedChannel && !isDM {
			if d.connector.Config.LogWhenDroppingMessages {
				log.Debug().
					Str("channel_id", evt.ChannelID).
					Str("message_id", evt.ID).
					Bool("route_uncertain", route != nil && route.Uncertain).
					Bool("from_channel_known", route != nil && route.FromChannel != nil).
					Bool("from_thread_known", route != nil && route.FromThread != nil).
					Msg("Dropping message for non-bridged channel")
			}
			return
		}
		if d.isOwnRelayWebhookMessage(ctx, evt.Message, route) {
			log.Debug().Msg("Dropping message from own relay webhook")
			return
		}

		if evt.Message.Type == discordgo.MessageTypeGuildMemberJoin {
			d.userCache.UpdateWithMessage(evt.Message)
			d.handleGuildMemberJoinMessage(ctx, evt.Message, route)
			return
		}

		if err := d.upsertThreadInfoFromMessage(ctx, evt.Message); err != nil {
			log.Err(err).Msg("Failed to persist thread info from message create")
		}
		d.userCache.UpdateWithMessage(evt.Message)

		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, route, bridgev2.RemoteEventMessage)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.MessageUpdate:
		ctx, log := messageCtx(ctx, evt.Message)
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		if d.isOwnRelayWebhookMessage(ctx, evt.Message, route) {
			log.Debug().Msg("Dropping message update from own relay webhook")
			return
		}

		if err := d.upsertThreadInfoFromMessage(ctx, evt.Message); err != nil {
			log.Err(err).Str("message_id", evt.ID).Msg("Failed to persist thread info from message update")
		}

		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, route, bridgev2.RemoteEventEdit)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.UserUpdate:
		// The current user changed. (This is not sent out for anyone else.)
		log.Info().Msg("Current user was updated")

		// discordgo does not update State.User for us. This is probably a bug.
		// Do it ourselves in the meantime.
		var oldFlags int
		{
			state := d.Session.State
			state.Lock()
			oldFlags = d.Session.State.User.Flags
			*d.Session.State.User = *evt.User
			state.Unlock()
		}
		d.userCache.UpdateWithUserUpdate(evt)
		user := evt.User

		if oldFlags != user.Flags {
			log.Info().
				Int("old_user_flags", oldFlags).
				Int("new_user_flags", user.Flags).
				Msg("User flags were updated")
		}
		d.pokeVitals(ctx)
		d.syncRemoteProfile(ctx)
		d.sendCurrentState(ctx)
	case *discordgo.MessageDelete:
		ctx, _ := messageCtx(ctx, evt.Message)
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}

		wrappedEvt := d.wrapDiscordMessage(ctx, evt.Message, route, bridgev2.RemoteEventMessageRemove)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	// TODO *discordgo.MessageDeleteBulk
	case *discordgo.MessageReactionAdd:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		wrappedEvt, err := d.wrapDiscordReaction(ctx, evt.MessageReaction, route, true)
		if err != nil {
			log.Err(err).Msg("Dropping incoming reaction due to error")
		} else {
			d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, wrappedEvt)
		}
	// TODO case *discordgo.MessageReactionRemoveAll:
	// TODO case *discordgo.MessageReactionRemoveEmoji: (needs impl. in discordgo)
	case *discordgo.MessageReactionRemove:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		if !bridged {
			return
		}
		wrappedEvt, err := d.wrapDiscordReaction(ctx, evt.MessageReaction, route, false)
		if err != nil {
			log.Err(err).Msg("Dropping incoming reaction removal due to error")
		} else {
			d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, wrappedEvt)
		}
	// NOTE: Relationship updates are also handled in handleDiscordStateEvent,
	// which is synchronously invoked before this one. This is to ensure
	// coherence in the face of concurrency, because this method is always
	// dispatched on a new goroutine.
	case *discordgo.RelationshipAdd:
		d.handleRelationshipNickChange(ctx, evt.ID, evt.Nickname)
	case *discordgo.RelationshipUpdate:
		d.handleRelationshipNickChange(ctx, evt.ID, evt.Nickname)
	case *discordgo.RelationshipRemove:
		d.handleRelationshipNickChange(ctx, evt.ID, "")
	case *discordgo.PresenceUpdate:
		d.handlePresenceUpdate(ctx, evt)
	case *discordgo.MessageAck:
		bridged, route := d.channelIsBridged(ctx, evt.ChannelID)
		d.handleMessageAck(ctx, evt, bridged, route)
	case *discordgo.UserGuildSettingsUpdate:
		d.handleUserGuildSettingsUpdate(ctx, evt)
	case *discordgo.GuildDelete:
		if evt.Unavailable {
			log.Warn().Str("guild_id", evt.ID).Msg("Guild became unavailable")
			// Leave the portals alone if the guild only went away due to
			// availability (a Discord outage).
			return
		}
		d.queueGuildDeletion(ctx, evt.ID)
	}
}
