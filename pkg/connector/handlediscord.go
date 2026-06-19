// Discord gateway event dispatch to QueueRemoteEvent.
//
// This file translates discordgo gateway events into bridgev2 RemoteEvents and
// enqueues them via dc.userLogin.QueueRemoteEvent. It also maintains the
// in-memory role cache (ar H10) and the connector's discorddb emoji/role tables.
//
// Thread model (ar H7): thread MESSAGE_CREATE events target the PARENT channel
// portal; the message carries ConvertedMessage.ThreadRoot, not a sub-portal.
//
// Bridging-mode gating (FR-45): MESSAGE_CREATE sets ShouldCreatePortal() based
// on the guild-space portal's PortalMeta.GuildBridgingMode. DMs always create.
package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// ---------------------------------------------------------------------------
// dispatchRemoteEvent — main gateway-event router
// ---------------------------------------------------------------------------

// dispatchRemoteEvent translates a gateway event into one or more bridgev2
// RemoteEvents and enqueues them. Events affecting only connector-internal
// caches (role/emoji) are handled entirely here without emitting a RemoteEvent.
func (dc *DiscordClient) dispatchRemoteEvent(rawEvt any) {
	ctx := dc.br.BackgroundCtx
	switch evt := rawEvt.(type) {

	// --- Message events ---

	case *discordgo.MessageCreate:
		dc.handleMessageCreate(ctx, evt)
	case *discordgo.MessageUpdate:
		dc.handleMessageUpdate(ctx, evt)
	case *discordgo.MessageDelete:
		dc.handleMessageDelete(ctx, evt)
	case *discordgo.MessageDeleteBulk:
		dc.handleMessageDeleteBulk(ctx, evt)

	// --- Reaction events ---

	case *discordgo.MessageReactionAdd:
		dc.handleReactionAdd(ctx, evt)
	case *discordgo.MessageReactionRemove:
		dc.handleReactionRemove(ctx, evt)

	// --- Typing + read receipts ---

	case *discordgo.TypingStart:
		dc.handleTypingStart(ctx, evt)
	case *discordgo.MessageAck:
		dc.handleMessageAck(ctx, evt)

	// --- Channel-level metadata ---

	case *discordgo.ChannelCreate:
		dc.handleChannelCreate(ctx, evt)
	case *discordgo.ChannelUpdate:
		dc.handleChannelUpdate(ctx, evt)
	case *discordgo.ChannelDelete:
		dc.handleChannelDelete(ctx, evt)
	case *discordgo.ChannelRecipientAdd:
		dc.handleChannelRecipientAdd(ctx, evt)
	case *discordgo.ChannelRecipientRemove:
		dc.handleChannelRecipientRemove(ctx, evt)

	// --- Guild-level metadata ---

	case *discordgo.GuildCreate:
		dc.handleGuildCreate(ctx, evt)
	case *discordgo.GuildUpdate:
		dc.handleGuildUpdate(ctx, evt)
	case *discordgo.GuildDelete:
		dc.handleGuildDelete(ctx, evt)

	// --- Role cache updates (FR-10, ar H10) ---

	case *discordgo.GuildRoleCreate:
		dc.upsertRole(ctx, evt.GuildID, evt.Role)
	case *discordgo.GuildRoleUpdate:
		dc.upsertRole(ctx, evt.GuildID, evt.Role)
	case *discordgo.GuildRoleDelete:
		dc.deleteRole(ctx, evt.GuildID, evt.RoleID)

	// --- Emoji cache updates (FR-67) ---

	case *discordgo.GuildEmojisUpdate:
		dc.syncGuildEmojis(ctx, evt.GuildID, evt.Emojis)

	// --- Thread list sync (FR-42) ---

	case *discordgo.ThreadListSync:
		dc.handleThreadListSync(ctx, evt)

	// Silently ignore the generic wrapper event (undocumented gateway events).
	case *discordgo.Event:

	default:
		dc.logger().Debug().Type("event_type", rawEvt).Msg("Unhandled Discord event")
	}
}

// ---------------------------------------------------------------------------
// dispatchReady — READY payload → ChatResync events for all guilds/channels
// ---------------------------------------------------------------------------

// dispatchReady emits ChatResync RemoteEvents for every guild and private
// channel present in the READY payload, seeding portal-resync and optional
// missed-backfill (FR-42/45). It also pre-populates the role/emoji caches.
func (dc *DiscordClient) dispatchReady(r *discordgo.Ready) {
	ctx := dc.br.BackgroundCtx

	// Populate role and emoji caches from the guild list carried in READY.
	for _, guild := range r.Guilds {
		if len(guild.Roles) > 0 {
			dc.syncGuildRoles(ctx, guild.ID, guild.Roles)
		}
		if len(guild.Emojis) > 0 {
			dc.syncGuildEmojis(ctx, guild.ID, guild.Emojis)
		}
		dc.queueGuildResync(ctx, guild)
	}

	// Private channels (DMs and group DMs).
	for _, ch := range r.PrivateChannels {
		dc.queueChannelResync(ctx, ch, true /* isDM */)
	}

	// Process read states carried in READY (FR-37 / OQ-13).
	if r.ReadState != nil && r.ReadState.Version > dc.Meta().ReadStateVersion {
		for _, entry := range r.ReadState.Entries {
			if entry.LastMessageID == "" {
				continue
			}
			dc.queueReadReceipt(ctx, entry.ID, string(entry.LastMessageID), r.ReadState.Version)
		}
		meta := dc.Meta()
		meta.ReadStateVersion = r.ReadState.Version
		if saveErr := dc.userLogin.Save(ctx); saveErr != nil {
			dc.logger().Err(saveErr).Msg("Failed to save read-state version from READY")
		}
	}
}

// ---------------------------------------------------------------------------
// Message events
// ---------------------------------------------------------------------------

// portalKeyForMessage returns the PortalKey for a message event. For thread
// messages the portal is the PARENT channel (in-room thread model — ar H7):
// if parentID is non-empty the parent channel ID is used instead.
func (dc *DiscordClient) portalKeyForMessage(channelID, parentID, guildID string) networkid.PortalKey {
	targetID := channelID
	if parentID != "" {
		targetID = parentID
	}
	isDM := guildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	return discordid.MakePortalKey(targetID, receiver, isDM)
}

// guildBridgingModeForChannel returns the BridgingMode stored in the
// guild-space portal's metadata (FR-45). DMs always return BridgingModeEverything.
func (dc *DiscordClient) guildBridgingModeForChannel(ctx context.Context, guildID string) BridgingMode {
	if guildID == "" {
		return BridgingModeEverything
	}
	guildPortalKey := networkid.PortalKey{ID: discordid.MakeGuildPortalID(guildID)}
	portal, err := dc.br.GetExistingPortalByKey(ctx, guildPortalKey)
	if err != nil || portal == nil {
		return BridgingModeNothing
	}
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		return BridgingModeNothing
	}
	return meta.GuildBridgingMode
}

// shouldCreatePortalForGuild returns true when the bridging mode indicates new
// portals should be auto-created on first message (FR-45).
func shouldCreatePortalForGuild(mode BridgingMode) bool {
	return mode == BridgingModeCreateOnMessage || mode == BridgingModeEverything
}

// threadParentID looks up the parent channel ID for a thread channel ID using
// the gateway session state cache. Returns "" for non-thread channels.
func (dc *DiscordClient) threadParentID(channelID string) string {
	if dc.Session == nil || dc.Session.State == nil {
		return ""
	}
	ch, err := dc.Session.State.Channel(channelID)
	if err != nil || ch == nil || !ch.IsThread() {
		return ""
	}
	return ch.ParentID
}

func (dc *DiscordClient) handleMessageCreate(ctx context.Context, evt *discordgo.MessageCreate) {
	if evt.Message == nil {
		return
	}

	parentID := dc.threadParentID(evt.ChannelID)
	portalKey := dc.portalKeyForMessage(evt.ChannelID, parentID, evt.GuildID)
	mode := dc.guildBridgingModeForChannel(ctx, evt.GuildID)

	// Build the thread root MessageID only when routing a thread message to the
	// parent portal. The root is the thread channel's root message, identified
	// via the referenced message if present.
	var threadRootID *networkid.MessageID
	if parentID != "" && evt.ReferencedMessage != nil {
		rootID := discordid.MakeMessageID(parentID, evt.ReferencedMessage.ID)
		threadRootID = &rootID
	}

	msgEvt := &simplevent.Message[*discordgo.MessageCreate]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_message_id", evt.ID).
					Str("discord_guild_id", evt.GuildID)
			},
			PortalKey:    portalKey,
			Sender:       discordEventSender(evt.Author),
			CreatePortal: shouldCreatePortalForGuild(mode),
			Timestamp:    discordid.SnowflakeToTime(evt.ID),
		},
		Data: evt,
		ID:   discordid.MakeMessageID(evt.ChannelID, evt.ID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *discordgo.MessageCreate) (*bridgev2.ConvertedMessage, error) {
			// TODO(group4-4.2): replace with convertdiscord.ConvertMessage.
			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{
					{
						ID:   "",
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgText,
							Body:    data.Content,
						},
					},
				},
				ThreadRoot: threadRootID,
			}, nil
		},
	}
	dc.userLogin.QueueRemoteEvent(msgEvt)
}

func (dc *DiscordClient) handleMessageUpdate(ctx context.Context, evt *discordgo.MessageUpdate) {
	if evt.Message == nil {
		return
	}

	parentID := dc.threadParentID(evt.ChannelID)
	portalKey := dc.portalKeyForMessage(evt.ChannelID, parentID, evt.GuildID)

	msgEvt := &simplevent.Message[*discordgo.MessageUpdate]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventEdit,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_message_id", evt.ID)
			},
			PortalKey: portalKey,
			Sender:    discordEventSender(evt.Author),
			Timestamp: time.Now(),
		},
		Data:          evt,
		ID:            discordid.MakeMessageID(evt.ChannelID, evt.ID),
		TargetMessage: discordid.MakeMessageID(evt.ChannelID, evt.ID),
		ConvertEditFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *discordgo.MessageUpdate) (*bridgev2.ConvertedEdit, error) {
			// TODO(group4-4.2): replace with convertdiscord.ConvertEdit.
			if len(existing) == 0 {
				return &bridgev2.ConvertedEdit{}, nil
			}
			return &bridgev2.ConvertedEdit{
				ModifiedParts: []*bridgev2.ConvertedEditPart{
					{
						Part: existing[0],
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgText,
							Body:    data.Content,
						},
					},
				},
			}, nil
		},
	}
	dc.userLogin.QueueRemoteEvent(msgEvt)
}

func (dc *DiscordClient) handleMessageDelete(ctx context.Context, evt *discordgo.MessageDelete) {
	if evt.Message == nil {
		return
	}

	parentID := dc.threadParentID(evt.ChannelID)
	portalKey := dc.portalKeyForMessage(evt.ChannelID, parentID, evt.GuildID)

	dc.userLogin.QueueRemoteEvent(&simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessageRemove,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_message_id", evt.ID)
			},
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		TargetMessage: discordid.MakeMessageID(evt.ChannelID, evt.ID),
	})
}

func (dc *DiscordClient) handleMessageDeleteBulk(ctx context.Context, evt *discordgo.MessageDeleteBulk) {
	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ChannelID, receiver, isDM)

	for _, msgID := range evt.Messages {
		localMsgID := msgID // capture for closure
		dc.userLogin.QueueRemoteEvent(&simplevent.MessageRemove{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventMessageRemove,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("discord_channel_id", evt.ChannelID).
						Str("discord_message_id", localMsgID)
				},
				PortalKey: portalKey,
				Timestamp: time.Now(),
			},
			TargetMessage: discordid.MakeMessageID(evt.ChannelID, localMsgID),
		})
	}
}

// ---------------------------------------------------------------------------
// Reaction events
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleReactionAdd(ctx context.Context, evt *discordgo.MessageReactionAdd) {
	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ChannelID, receiver, isDM)
	emojiID, emojiStr := discordEmojiToNetworkID(&evt.Emoji)

	dc.userLogin.QueueRemoteEvent(&simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReaction,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_message_id", evt.MessageID).
					Str("discord_user_id", evt.UserID)
			},
			PortalKey: portalKey,
			Sender:    bridgev2.EventSender{Sender: discordid.MakeUserID(evt.UserID)},
			Timestamp: time.Now(),
		},
		TargetMessage: discordid.MakeMessageID(evt.ChannelID, evt.MessageID),
		EmojiID:       emojiID,
		Emoji:         emojiStr,
	})
}

func (dc *DiscordClient) handleReactionRemove(ctx context.Context, evt *discordgo.MessageReactionRemove) {
	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ChannelID, receiver, isDM)
	emojiID, _ := discordEmojiToNetworkID(&evt.Emoji)

	dc.userLogin.QueueRemoteEvent(&simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReactionRemove,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_message_id", evt.MessageID).
					Str("discord_user_id", evt.UserID)
			},
			PortalKey: portalKey,
			Sender:    bridgev2.EventSender{Sender: discordid.MakeUserID(evt.UserID)},
			Timestamp: time.Now(),
		},
		TargetMessage: discordid.MakeMessageID(evt.ChannelID, evt.MessageID),
		EmojiID:       emojiID,
	})
}

// discordEmojiToNetworkID converts a discordgo.Emoji to the (EmojiID, display
// string) pair expected by simplevent.Reaction. Custom emojis carry a snowflake
// EmojiID; standard Unicode emoji return an empty EmojiID.
func discordEmojiToNetworkID(e *discordgo.Emoji) (networkid.EmojiID, string) {
	if e.ID != "" {
		return networkid.EmojiID(e.ID), e.MessageFormat()
	}
	return "", e.Name
}

// ---------------------------------------------------------------------------
// Typing (FR-37)
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleTypingStart(ctx context.Context, evt *discordgo.TypingStart) {
	// Suppress the bridge user's own typing events.
	if evt.UserID == string(dc.userLogin.ID) {
		return
	}

	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ChannelID, receiver, isDM)

	dc.userLogin.QueueRemoteEvent(&simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventTyping,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("discord_user_id", evt.UserID)
			},
			PortalKey: portalKey,
			Sender:    bridgev2.EventSender{Sender: discordid.MakeUserID(evt.UserID)},
			Timestamp: time.Unix(int64(evt.Timestamp), 0),
		},
		Timeout: 10 * time.Second,
		Type:    bridgev2.TypingTypeText,
	})
}

// ---------------------------------------------------------------------------
// Read receipts / MessageAck (FR-37, OQ-13)
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleMessageAck(ctx context.Context, evt *discordgo.MessageAck) {
	dc.queueReadReceipt(ctx, evt.ChannelID, evt.MessageID, evt.Version)
}

// queueReadReceipt emits a RemoteEventReadReceipt for the given channel +
// message pair and persists the updated ReadStateVersion when the incoming
// version is newer (FR-37).
func (dc *DiscordClient) queueReadReceipt(ctx context.Context, channelID, messageID string, version int) {
	if channelID == "" || messageID == "" {
		return
	}

	// Determine isDM from the session state cache; fall back to false.
	isDM := false
	if dc.Session != nil && dc.Session.State != nil {
		if ch, err := dc.Session.State.Channel(channelID); err == nil && ch != nil {
			isDM = ch.GuildID == ""
		}
	}
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(channelID, receiver, isDM)
	msgID := discordid.MakeMessageID(channelID, messageID)

	dc.userLogin.QueueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReadReceipt,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", channelID).
					Str("discord_message_id", messageID)
			},
			PortalKey: portalKey,
			Sender: bridgev2.EventSender{
				Sender:   discordid.MakeUserID(string(dc.userLogin.ID)),
				IsFromMe: true,
			},
			Timestamp: time.Now(),
		},
		LastTarget: msgID,
	})

	// Persist updated read-state version.
	if version > 0 {
		meta := dc.Meta()
		if version > meta.ReadStateVersion {
			meta.ReadStateVersion = version
			if saveErr := dc.userLogin.Save(ctx); saveErr != nil {
				dc.logger().Err(saveErr).Msg("Failed to save read-state version")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Channel events
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleChannelCreate(ctx context.Context, evt *discordgo.ChannelCreate) {
	if evt.Channel == nil {
		return
	}
	dc.queueChannelResync(ctx, evt.Channel, evt.GuildID == "")
}

func (dc *DiscordClient) handleChannelUpdate(ctx context.Context, evt *discordgo.ChannelUpdate) {
	if evt.Channel == nil {
		return
	}
	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ID, receiver, isDM)

	dc.userLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatInfoChange,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ID)
			},
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		// ChatInfo nil signals "re-fetch from Discord REST" in the full
		// chatinfo.go implementation (TODO group4-4.3).
		ChatInfoChange: &bridgev2.ChatInfoChange{},
	})
}

func (dc *DiscordClient) handleChannelDelete(ctx context.Context, evt *discordgo.ChannelDelete) {
	if evt.Channel == nil {
		return
	}
	isDM := evt.GuildID == ""
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(evt.ID, receiver, isDM)

	dc.userLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatDelete,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ID)
			},
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
	})
}

// handleChannelRecipientAdd emits a ChatInfoChange adding the new recipient to
// the group DM's member list.
func (dc *DiscordClient) handleChannelRecipientAdd(ctx context.Context, evt *discordgo.ChannelRecipientAdd) {
	portalKey := discordid.MakePortalKey(evt.ChannelID, dc.userLogin.ID, true)
	dc.userLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatInfoChange,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("added_user_id", evt.User.ID)
			},
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				MemberMap: map[networkid.UserID]bridgev2.ChatMember{
					discordid.MakeUserID(evt.User.ID): {
						EventSender: bridgev2.EventSender{
							Sender: discordid.MakeUserID(evt.User.ID),
						},
						Membership: "join",
					},
				},
			},
		},
	})
}

// handleChannelRecipientRemove emits a ChatInfoChange marking the removed user
// as having left the group DM.
func (dc *DiscordClient) handleChannelRecipientRemove(ctx context.Context, evt *discordgo.ChannelRecipientRemove) {
	portalKey := discordid.MakePortalKey(evt.ChannelID, dc.userLogin.ID, true)
	dc.userLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatInfoChange,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", evt.ChannelID).
					Str("removed_user_id", evt.User.ID)
			},
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				MemberMap: map[networkid.UserID]bridgev2.ChatMember{
					discordid.MakeUserID(evt.User.ID): {
						EventSender: bridgev2.EventSender{
							Sender: discordid.MakeUserID(evt.User.ID),
						},
						Membership: "leave",
					},
				},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// Guild events
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleGuildCreate(ctx context.Context, evt *discordgo.GuildCreate) {
	if evt.Guild == nil {
		return
	}
	dc.logger().Info().
		Str("guild_id", evt.ID).
		Str("guild_name", evt.Name).
		Bool("unavailable", evt.Unavailable).
		Msg("Got guild create event")

	if len(evt.Roles) > 0 {
		dc.syncGuildRoles(ctx, evt.ID, evt.Roles)
	}
	if len(evt.Emojis) > 0 {
		dc.syncGuildEmojis(ctx, evt.ID, evt.Emojis)
	}

	dc.queueGuildResync(ctx, evt.Guild)
}

func (dc *DiscordClient) handleGuildUpdate(ctx context.Context, evt *discordgo.GuildUpdate) {
	if evt.Guild == nil {
		return
	}
	dc.logger().Debug().Str("guild_id", evt.ID).Msg("Got guild update event")

	guildPortalKey := networkid.PortalKey{ID: discordid.MakeGuildPortalID(evt.ID)}
	dc.userLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatInfoChange,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_guild_id", evt.ID)
			},
			PortalKey: guildPortalKey,
			Timestamp: time.Now(),
		},
		// TODO(group4-4.3): build ChatInfo directly from evt.Guild.
		ChatInfoChange: &bridgev2.ChatInfoChange{},
	})
}

func (dc *DiscordClient) handleGuildDelete(ctx context.Context, evt *discordgo.GuildDelete) {
	if evt.Guild == nil {
		return
	}
	if evt.Unavailable {
		dc.logger().Info().Str("guild_id", evt.ID).
			Msg("Ignoring guild delete event with unavailable flag")
		return
	}
	dc.logger().Info().Str("guild_id", evt.ID).Msg("Got guild delete event")

	guildPortalKey := networkid.PortalKey{ID: discordid.MakeGuildPortalID(evt.ID)}
	dc.userLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatDelete,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_guild_id", evt.ID)
			},
			PortalKey: guildPortalKey,
			Timestamp: time.Now(),
		},
		// Cascade to channel portals under this guild.
		Children: true,
	})
}

// ---------------------------------------------------------------------------
// Thread list sync (FR-42)
// ---------------------------------------------------------------------------

func (dc *DiscordClient) handleThreadListSync(ctx context.Context, evt *discordgo.ThreadListSync) {
	for _, thread := range evt.Threads {
		if thread.ParentID == "" {
			continue
		}
		// Route to the PARENT portal (in-room thread model, ar H7).
		portalKey := discordid.MakePortalKey(thread.ParentID, "", false)
		latestTS := discordid.SnowflakeToTime(thread.LastMessageID)

		localThread := thread // capture for closure
		dc.userLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("discord_thread_id", localThread.ID).
						Str("discord_parent_id", localThread.ParentID)
				},
				PortalKey: portalKey,
				Timestamp: time.Now(),
			},
			LatestMessageTS: latestTS,
		})
	}
}

// ---------------------------------------------------------------------------
// queueChannelResync — ChatResync for a single channel
// ---------------------------------------------------------------------------

// queueChannelResync enqueues a ChatResync for the given channel. isDM should
// be true for DMs and group DMs so that the receiver is set on the PortalKey.
func (dc *DiscordClient) queueChannelResync(ctx context.Context, ch *discordgo.Channel, isDM bool) {
	var receiver networkid.UserLoginID
	if isDM {
		receiver = dc.userLogin.ID
	}
	portalKey := discordid.MakePortalKey(ch.ID, receiver, isDM)
	latestTS := discordid.SnowflakeToTime(ch.LastMessageID)

	dc.userLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatResync,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_channel_id", ch.ID).
					Str("discord_channel_type", fmt.Sprintf("%d", ch.Type))
			},
			PortalKey: portalKey,
			// DMs pre-create on resync; guild channels honour bridging mode.
			CreatePortal: isDM,
			Timestamp:    time.Now(),
		},
		LatestMessageTS: latestTS,
	})
}

// ---------------------------------------------------------------------------
// queueGuildResync — ChatResync for a guild and its channels
// ---------------------------------------------------------------------------

// queueGuildResync emits ChatResync events for the guild-space portal first
// (ar M4: space must exist before children set ParentID), then for each
// bridgeable channel.
func (dc *DiscordClient) queueGuildResync(ctx context.Context, guild *discordgo.Guild) {
	if guild == nil {
		return
	}
	guildPortalKey := networkid.PortalKey{ID: discordid.MakeGuildPortalID(guild.ID)}

	// Emit the guild-space resync first.
	dc.userLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatResync,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("discord_guild_id", guild.ID)
			},
			PortalKey: guildPortalKey,
			Timestamp: time.Now(),
		},
	})

	// Emit per-channel resyncs, gated by bridging mode.
	mode := dc.guildBridgingModeForChannel(ctx, guild.ID)
	for _, ch := range guild.Channels {
		if !isChannelBridgeable(ch) {
			continue
		}
		chPortalKey := discordid.MakePortalKey(ch.ID, "", false)
		latestTS := discordid.SnowflakeToTime(ch.LastMessageID)

		localCh := ch // capture for closure
		dc.userLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("discord_channel_id", localCh.ID).
						Str("discord_guild_id", guild.ID)
				},
				PortalKey:    chPortalKey,
				CreatePortal: mode == BridgingModeEverything,
				Timestamp:    time.Now(),
			},
			LatestMessageTS: latestTS,
		})
	}
}

// isChannelBridgeable returns true for channel types that can produce a Matrix
// portal. Ported from legacy user.channelIsBridgeable.
func isChannelBridgeable(ch *discordgo.Channel) bool {
	switch ch.Type {
	case discordgo.ChannelTypeGuildText,
		discordgo.ChannelTypeGuildNews,
		discordgo.ChannelTypeDM,
		discordgo.ChannelTypeGroupDM,
		discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Role cache (FR-10, ar H10) — DB-backed with in-memory write-through
// ---------------------------------------------------------------------------

// upsertRole persists a single guild role to the DB and updates the in-memory
// cache. Called from GUILD_CREATE (via syncGuildRoles) and GUILD_ROLE_*.
func (dc *DiscordClient) upsertRole(ctx context.Context, guildID string, role *discordgo.Role) {
	if role == nil {
		return
	}
	var icon *string
	if role.Icon != "" {
		icon = &role.Icon
	}
	dbRole := &discorddb.Role{
		GuildID:     guildID,
		RoleID:      role.ID,
		Name:        role.Name,
		Icon:        icon,
		Mentionable: role.Mentionable,
		Managed:     role.Managed,
		Hoist:       role.Hoist,
		Color:       role.Color,
		Position:    role.Position,
		Permissions: role.Permissions,
	}
	if err := dc.connector.DB.Role.Upsert(ctx, dbRole); err != nil {
		dc.logger().Err(err).
			Str("guild_id", guildID).Str("role_id", role.ID).
			Msg("Failed to upsert role to DB")
	}

	// Keep the in-memory placeholder up to date (role name for hot-path
	// mention rendering — the full typed cache lives in Group 4's convertdiscord).
	cacheKey := guildID + "-" + role.ID
	dc.connector.roleCacheMu.Lock()
	dc.connector.roleCache[cacheKey] = role.Name
	dc.connector.roleCacheMu.Unlock()
}

// deleteRole removes a role from the DB and the in-memory cache.
func (dc *DiscordClient) deleteRole(ctx context.Context, guildID, roleID string) {
	if err := dc.connector.DB.Role.Delete(ctx, guildID, roleID); err != nil {
		dc.logger().Err(err).
			Str("guild_id", guildID).Str("role_id", roleID).
			Msg("Failed to delete role from DB")
	}
	cacheKey := guildID + "-" + roleID
	dc.connector.roleCacheMu.Lock()
	delete(dc.connector.roleCache, cacheKey)
	dc.connector.roleCacheMu.Unlock()
}

// syncGuildRoles bulk-upserts all roles for a guild. Called on GUILD_CREATE
// and reconnect.
func (dc *DiscordClient) syncGuildRoles(ctx context.Context, guildID string, roles []*discordgo.Role) {
	for _, role := range roles {
		dc.upsertRole(ctx, guildID, role)
	}
}

// ---------------------------------------------------------------------------
// Emoji cache (FR-67)
// ---------------------------------------------------------------------------

// syncGuildEmojis bulk-upserts custom emojis for a guild. Called on
// GUILD_CREATE and GUILD_EMOJIS_UPDATE (standard Unicode emoji have no ID and
// are skipped).
func (dc *DiscordClient) syncGuildEmojis(ctx context.Context, guildID string, emojis []*discordgo.Emoji) {
	for _, emoji := range emojis {
		if emoji.ID == "" {
			continue
		}
		dbEmoji := &discorddb.Emoji{
			GuildID:  guildID,
			EmojiID:  emoji.ID,
			Name:     emoji.Name,
			Animated: emoji.Animated,
		}
		if err := dc.connector.DB.Emoji.Upsert(ctx, dbEmoji); err != nil {
			dc.logger().Err(err).
				Str("guild_id", guildID).Str("emoji_id", emoji.ID).
				Msg("Failed to upsert emoji to DB")
		}
	}
}

// ---------------------------------------------------------------------------
// discordEventSender — build EventSender from a *discordgo.User
// ---------------------------------------------------------------------------

// discordEventSender returns an EventSender for the given Discord user. Nil
// user (system messages) returns an empty sender so the framework selects the
// bot intent.
func discordEventSender(user *discordgo.User) bridgev2.EventSender {
	if user == nil {
		return bridgev2.EventSender{}
	}
	return bridgev2.EventSender{
		Sender: discordid.MakeUserID(user.ID),
	}
}
