// HandleMatrix* outbound handlers (Matrix → Discord).
//
// Implements Tasks 5.2 (send/edit/delete) and 5.3 (reactions, read receipts,
// typing, room name/topic) of the bridgev2 migration. These satisfy the
// EditHandlingNetworkAPI / RedactionHandlingNetworkAPI /
// ReactionHandlingNetworkAPI / ReadReceiptHandlingNetworkAPI /
// TypingHandlingNetworkAPI / RoomNameHandlingNetworkAPI /
// RoomTopicHandlingNetworkAPI interfaces on *DiscordClient, plus the required
// NetworkAPI.HandleMatrixMessage.
//
// Ported from the legacy top-level portal.go handlers (handleMatrixMessage,
// handleMatrixReaction, handleMatrixRedaction, HandleMatrixReadReceipt,
// HandleMatrixTyping). The framework now does the bulk of the bookkeeping
// (event/portal/sender resolution, DB lookups, reaction dedup, status
// reporting), so these methods only do the Discord-side network calls and return
// the bits the framework needs to persist.
//
// The Matrix→Discord *content* conversion (HTML→markdown, pills→mentions,
// media download+upload, reply refs, allowed-mentions/silent-reply) is owned by
// convertmatrix.go (Task 5.1): convertMatrixToDiscord / convertMatrixEditToDiscord.
// This file does NOT duplicate that logic — it only routes (channel vs thread),
// guards (FR-75 voice), and persists.
package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/variationselector"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// Outbound handler errors. Mirror the legacy portal.go error set; the framework
// surfaces these as message-send-status errors to the user.
var (
	errUserNotReceiver   = errors.New("user is not the portal receiver")
	errUnknownEditTarget = errors.New("unknown edit target")
	errUnknownEmoji      = errors.New("unknown emoji")
	errTargetNotFound    = errors.New("target message/reaction not found")
	// errVoiceChannelSend is returned for FR-75: voice channels can't host text.
	errVoiceChannelSend = errors.New("can't send messages in a voice channel")
)

// portalChannelInfo bundles the per-portal Discord routing data the outbound
// handlers need: the channel snowflake, the guild snowflake (empty for DMs),
// and the channel type (for the FR-75 voice guard and DM gating).
type portalChannelInfo struct {
	channelID   string
	guildID     string
	channelType discordgo.ChannelType
	isDM        bool
}

// portalInfo extracts the Discord routing data from a portal's metadata.
func portalInfo(portal *bridgev2.Portal) portalChannelInfo {
	info := portalChannelInfo{
		channelID: discordid.ParsePortalID(portal.ID),
	}
	if meta, ok := portal.Metadata.(*PortalMeta); ok && meta != nil {
		info.guildID = meta.GuildID
		info.channelType = meta.ChannelType
		info.isDM = meta.ChannelType == discordgo.ChannelTypeDM ||
			meta.ChannelType == discordgo.ChannelTypeGroupDM
	}
	return info
}

// isVoiceChannel reports whether the portal is a voice/stage channel, which
// cannot carry text messages (FR-75).
func (info portalChannelInfo) isVoiceChannel() bool {
	return info.channelType == discordgo.ChannelTypeGuildVoice ||
		info.channelType == discordgo.ChannelTypeGuildStageVoice
}

// threadChannelID returns the Discord thread channel ID for an outbound message
// whose framework-provided ThreadRoot points into a thread, plus whether the
// message is thread-scoped. In the in-room thread model (ar H7) a thread channel
// created from a message has channel ID == that message's snowflake, so the
// thread channel is the message-ID portion of the thread root's MessageID.
// Returns ("", false) for non-thread sends.
func threadChannelID(threadRoot *database.Message) (string, bool) {
	if threadRoot == nil {
		return "", false
	}
	_, starterMsgID, ok := discordid.ParseMessageID(threadRoot.ID)
	if !ok || starterMsgID == "" {
		return "", false
	}
	return starterMsgID, true
}

// messageThread returns the thread channel ID a stored message belongs to, or ""
// if it's in the main channel. A message inside a thread carries a ThreadRoot
// encoding the thread starter message, whose snowflake is the thread channel ID.
func messageThread(msg *database.Message) string {
	if msg == nil || msg.ThreadRoot == "" {
		return ""
	}
	_, starterMsgID, ok := discordid.ParseMessageID(msg.ThreadRoot)
	if !ok {
		return ""
	}
	return starterMsgID
}

// requestOpts builds the discordgo request options for an outbound call: the
// per-channel/thread referer (user tokens only — bots neither need nor should
// send it) plus the request context. Mirrors the legacy Portal.RefererOptIfUser.
func (dc *DiscordClient) requestOpts(ctx context.Context, info portalChannelInfo, threadID string) []discordgo.RequestOption {
	opts := []discordgo.RequestOption{discordgo.WithContext(ctx)}
	sess := dc.Session
	if sess == nil || !sess.IsUser {
		return opts
	}
	if threadID != "" && threadID != info.channelID {
		opts = append(opts, discordgo.WithThreadReferer(info.guildID, info.channelID, threadID))
	} else {
		opts = append(opts, discordgo.WithChannelReferer(info.guildID, info.channelID))
	}
	return opts
}

// checkDMReceiver guards DM portals: only the portal receiver (= this login's
// user) may send. Mirrors the legacy errUserNotReceiver check.
func (dc *DiscordClient) checkDMReceiver(portal *bridgev2.Portal, info portalChannelInfo) error {
	if info.isDM && portal.Receiver != dc.userLogin.ID {
		return errUserNotReceiver
	}
	return nil
}

// --- NetworkAPI (required): HandleMatrixMessage (Task 5.2) ---

// HandleMatrixMessage converts a Matrix message and sends it to the Discord
// channel (or thread channel). The conversion is delegated to convertmatrix.go;
// this method owns routing (thread vs channel), the FR-75 voice guard, and
// constructing the DB message row the framework persists with the new Discord
// message ID.
//
// Relay path (Task 6.2 / FR-44): when msg.OrigSender != nil (the framework has
// determined this is a relay send) AND the portal has webhook creds in
// PortalMeta, the message is sent via the Discord incoming webhook with a
// spoofed username/avatar derived from the original sender's Matrix profile.
// The relay login's own Discord session is not used in this case.
func (dc *DiscordClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if !dc.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)
	if err := dc.checkDMReceiver(msg.Portal, info); err != nil {
		return nil, err
	}

	// FR-75: voice channels can't carry text. Reject before any conversion or
	// network work so the user gets a clear status.
	if info.isVoiceChannel() {
		return nil, errVoiceChannelSend
	}

	// Route to the thread channel when the framework resolved a thread root.
	channelID := info.channelID
	threadID := ""
	if tid, isThread := threadChannelID(msg.ThreadRoot); isThread {
		threadID = tid
		channelID = tid
	}

	// --- Relay webhook path (Task 6.2) ---
	if isRelayWebhookSend(msg, msg.Portal) {
		return dc.handleRelayWebhookSend(ctx, msg, info, channelID, threadID)
	}

	// --- Normal user/bot session path ---
	sess := dc.Session
	if sess == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	// Convert the Matrix content into a Discord MessageSend. convertmatrix owns
	// HTML→markdown, pills, allowed-mentions/silent-reply, reply refs and media
	// upload. The bot intent is used to download Matrix-side media.
	result, err := convertMatrixToDiscord(ctx, dc.br, dc.br.Bot, sess, msg.Portal, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message: %w", err)
	}
	if result == nil || result.Send == nil {
		return nil, fmt.Errorf("conversion produced no Discord message")
	}
	sendReq := result.Send

	// convertmatrix builds the reply reference against the base channel; for a
	// thread send the reference must point at the thread channel.
	if sendReq.Reference != nil && threadID != "" {
		sendReq.Reference.ChannelID = threadID
	}

	// MSC3245 voice message → Discord voice-message flag.
	if result.IsVoiceMessage {
		flags := int(discordgo.MessageFlagsIsVoiceMessage)
		sendReq.Flags = &flags
	}

	sendReq.Nonce = generateNonce()

	sent, err := sess.ChannelMessageSendComplex(channelID, sendReq, dc.requestOpts(ctx, info, threadID)...)
	if err != nil {
		return nil, fmt.Errorf("failed to send message to Discord: %w", err)
	}
	if sent == nil {
		return nil, fmt.Errorf("Discord returned no message after send")
	}

	return &bridgev2.MatrixMessageResponse{
		DB: dc.dbMessageFromSent(msg.Portal, sent),
	}, nil
}

// handleRelayWebhookSend handles the relay webhook branch of HandleMatrixMessage.
// Called when msg.OrigSender != nil and the portal has webhook creds in PortalMeta.
// Converts the message using the relay (no-session) path in convertmatrix.go,
// builds the webhook payload with the spoofed username/avatar, and executes the
// Discord incoming webhook.
func (dc *DiscordClient) handleRelayWebhookSend(
	ctx context.Context,
	msg *bridgev2.MatrixMessage,
	info portalChannelInfo,
	channelID, threadID string,
) (*bridgev2.MatrixMessageResponse, error) {
	webhookID, webhookSecret := relayWebhookCreds(msg.Portal)
	if webhookID == "" {
		return nil, fmt.Errorf("relay send requested but portal has no webhook configured")
	}

	// Convert with sess=nil so convertmatrix skips user-only paths (CDN upload,
	// MessageReference) and treats this as a webhook send.
	result, err := convertMatrixToDiscord(ctx, dc.br, dc.br.Bot, nil, msg.Portal, msg)
	if err != nil {
		return nil, fmt.Errorf("relay: failed to convert message: %w", err)
	}
	if result == nil || result.Send == nil {
		return nil, fmt.Errorf("relay: conversion produced no Discord message")
	}
	sendReq := result.Send

	// Build the reply embed (replaces MessageReference for webhook sends).
	// convertmatrix leaves RelayEmbed nil for us to fill here.
	if msg.ReplyTo != nil {
		if embed := dc.makeRelayReplyEmbed(ctx, msg.Portal, msg.ReplyTo, channelID); embed != nil {
			sendReq.Embeds = append([]*discordgo.MessageEmbed{embed}, sendReq.Embeds...)
		}
	}

	// Derive the spoofed sender profile for this relay (FR-44).
	username, avatarURL := dc.getRelayUserMeta(ctx, msg.Portal, msg.OrigSender)

	params := &discordgo.WebhookParams{
		Content:         sendReq.Content,
		Username:        username,
		AvatarURL:       avatarURL,
		Files:           sendReq.Files,
		Components:      sendReq.Components,
		Embeds:          sendReq.Embeds,
		AllowedMentions: sendReq.AllowedMentions,
	}

	sent, err := sendViaWebhook(webhookID, webhookSecret, threadID, params)
	if err != nil {
		return nil, fmt.Errorf("relay: failed to execute webhook: %w", err)
	}
	if sent == nil {
		return nil, fmt.Errorf("relay: Discord returned no message after webhook execute")
	}

	// The SenderID for relay-sent messages is the webhook ID (mirrors legacy
	// dbMsg.SenderID = portal.RelayWebhookID).
	channelIDStr := discordid.ParsePortalID(msg.Portal.ID)
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       discordid.MakeMessageID(channelIDStr, sent.ID),
			SenderID: networkid.UserID(webhookID),
			Metadata: &MessageMeta{DiscordID: sent.ID},
		},
	}, nil
}

// dbMessageFromSent builds the database.Message row to persist for a freshly
// sent Discord message. The framework fills MXID/Room/SenderMXID/Timestamp/
// ReplyTo/ThreadRoot from the Matrix event; the connector supplies the
// network-specific ID / SenderID / Metadata. Single-part messages (text, or a
// single attachment collapsed into the text part by convertmatrix) use an empty
// PartID, matching the single-part collapse convention.
func (dc *DiscordClient) dbMessageFromSent(portal *bridgev2.Portal, sent *discordgo.Message) *database.Message {
	channelID := discordid.ParsePortalID(portal.ID)
	return &database.Message{
		ID:       discordid.MakeMessageID(channelID, sent.ID),
		SenderID: dc.GetUserID(),
		Metadata: &MessageMeta{DiscordID: sent.ID},
	}
}

// --- EditHandlingNetworkAPI (Task 5.2) ---

// HandleMatrixEdit converts a Matrix edit and patches the Discord message
// (FR-29/36). The framework supplies the edit target DB row. Only the
// text/allowed-mentions are sent (Discord can't edit attachments). On success
// the edit timestamp is recorded in the message metadata for ordering.
//
// Relay webhook path (Task 6.2 / FR-44): when the edit target message was
// originally sent by the relay webhook (SenderID == webhookID) and the portal
// has webhook creds, edit via the Discord webhook edit API instead of the user
// session.
func (dc *DiscordClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if !dc.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)
	if err := dc.checkDMReceiver(msg.Portal, info); err != nil {
		return err
	}
	if msg.EditTarget == nil {
		return errUnknownEditTarget
	}

	editChannelID, editMsgID, ok := discordIDsForMessage(msg.EditTarget)
	if !ok || editMsgID == "" {
		return fmt.Errorf("%w: unparseable target ID %q", errUnknownEditTarget, msg.EditTarget.ID)
	}
	threadID := ""
	if tid := messageThread(msg.EditTarget); tid != "" {
		threadID = tid
		editChannelID = tid
	}

	content, allowedMentions, err := convertMatrixEditToDiscord(dc.br, msg.Portal, msg)
	if err != nil {
		return fmt.Errorf("failed to convert edit: %w", err)
	}

	// --- Relay webhook edit path (FR-44) ---
	webhookID, webhookSecret := relayWebhookCreds(msg.Portal)
	if webhookID != "" && string(msg.EditTarget.SenderID) == webhookID {
		// The original message was sent by the relay webhook — edit via webhook API.
		// (threadID is unused for webhook edits: the message ID alone is sufficient.)
		_ = editChannelID // resolved above for the non-relay path; unused here
		_ = threadID
		sent, editErr := editViaWebhook(webhookID, webhookSecret, editMsgID, &discordgo.WebhookEdit{
			Content:         &content,
			AllowedMentions: allowedMentions,
		})
		if editErr != nil {
			return fmt.Errorf("relay: failed to edit webhook message: %w", editErr)
		}
		if sent != nil && sent.EditedTimestamp != nil {
			editTS := *sent.EditedTimestamp
			meta, _ := msg.EditTarget.Metadata.(*MessageMeta)
			if meta == nil {
				meta = &MessageMeta{DiscordID: editMsgID}
				msg.EditTarget.Metadata = meta
			}
			meta.EditTimestamp = &editTS
			msg.EditTarget.EditCount++
			if saveErr := dc.br.DB.Message.Update(ctx, msg.EditTarget); saveErr != nil {
				dc.logger().Warn().Err(saveErr).Msg("Failed to save edit timestamp after relay webhook edit")
			}
		}
		return nil
	}

	// --- Normal user/bot session path ---
	sess := dc.Session
	if sess == nil {
		return bridgev2.ErrNotLoggedIn
	}

	editReq := &discordgo.MessageEdit{
		ID:              editMsgID,
		Channel:         editChannelID,
		Content:         &content,
		AllowedMentions: allowedMentions,
	}
	sent, err := sess.ChannelMessageEditComplex(editReq, dc.requestOpts(ctx, info, threadID)...)
	if err != nil {
		return fmt.Errorf("failed to edit Discord message: %w", err)
	}

	// Record the edit timestamp for ordering (FR-29) and bump the edit count.
	if sent != nil && sent.EditedTimestamp != nil {
		editTS := *sent.EditedTimestamp
		meta, _ := msg.EditTarget.Metadata.(*MessageMeta)
		if meta == nil {
			meta = &MessageMeta{DiscordID: editMsgID}
			msg.EditTarget.Metadata = meta
		}
		meta.EditTimestamp = &editTS
		msg.EditTarget.EditCount++
		if err := dc.br.DB.Message.Update(ctx, msg.EditTarget); err != nil {
			dc.logger().Warn().Err(err).Msg("Failed to save edit timestamp after Matrix edit")
		}
	}
	return nil
}

// --- RedactionHandlingNetworkAPI (Task 5.2) ---

// HandleMatrixMessageRemove deletes a Discord message (FR-29). The framework
// supplies the target DB row.
//
// Relay webhook path (Task 6.2 / FR-44): when the target message was
// originally sent by the relay webhook (SenderID == webhookID), delete via the
// Discord webhook delete API. This mirrors the legacy
// `relayClient.WebhookMessageDelete` branch in portal.go.
func (dc *DiscordClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if !dc.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)
	if err := dc.checkDMReceiver(msg.Portal, info); err != nil {
		return err
	}
	if msg.TargetMessage == nil {
		return errTargetNotFound
	}

	delChannelID, delMsgID, ok := discordIDsForMessage(msg.TargetMessage)
	if !ok || delMsgID == "" {
		return fmt.Errorf("%w: unparseable target ID %q", errTargetNotFound, msg.TargetMessage.ID)
	}
	threadID := ""
	if tid := messageThread(msg.TargetMessage); tid != "" {
		threadID = tid
		delChannelID = tid
	}

	// --- Relay webhook delete path (FR-44) ---
	webhookID, webhookSecret := relayWebhookCreds(msg.Portal)
	if webhookID != "" && string(msg.TargetMessage.SenderID) == webhookID {
		// Original message was sent by the relay webhook — delete via webhook API.
		_ = delChannelID // not needed for webhook delete (message ID suffices)
		_ = threadID
		if err := deleteViaWebhook(webhookID, webhookSecret, delMsgID); err != nil {
			return fmt.Errorf("relay: failed to delete webhook message: %w", err)
		}
		return nil
	}

	// --- Normal user/bot session path ---
	sess := dc.Session
	if sess == nil {
		return bridgev2.ErrNotLoggedIn
	}

	if err := sess.ChannelMessageDelete(delChannelID, delMsgID, dc.requestOpts(ctx, info, threadID)...); err != nil {
		return fmt.Errorf("failed to delete Discord message: %w", err)
	}
	return nil
}

// discordIDsForMessage resolves the Discord channel and message snowflakes for a
// stored message. The channel comes from the MessageID prefix; the message
// snowflake is read from MessageMeta.DiscordID (falling back to the MessageID
// suffix). Returns ok=false if the MessageID has no "-" separator.
func discordIDsForMessage(msg *database.Message) (channelID, messageID string, ok bool) {
	channelID, messageID, ok = discordid.ParseMessageID(msg.ID)
	if !ok {
		return "", "", false
	}
	if meta, _ := msg.Metadata.(*MessageMeta); meta != nil && meta.DiscordID != "" {
		messageID = meta.DiscordID
	}
	return channelID, messageID, true
}

// --- ReactionHandlingNetworkAPI (Task 5.3) ---

// PreHandleMatrixReaction resolves the reaction emoji into the network
// representation the framework uses for de-duplication (FR-40). For a Unicode
// emoji it returns Emoji=<fully-qualified char> with an empty EmojiID; for a
// custom (mxc://) emoji it resolves the mxc back to a Discord emoji and returns
// EmojiID="<name>:<id>". Discord enforces one reaction per user per emoji, which
// the framework's (SenderID, EmojiID/Emoji) dedup already matches, so
// MaxReactions is left at 0 (Discord has no per-user global cap).
func (dc *DiscordClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	if !dc.IsLoggedIn() {
		return bridgev2.MatrixReactionPreResponse{}, bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)
	if err := dc.checkDMReceiver(msg.Portal, info); err != nil {
		return bridgev2.MatrixReactionPreResponse{}, err
	}

	resp := bridgev2.MatrixReactionPreResponse{
		SenderID:     dc.GetUserID(),
		MaxReactions: 0,
	}

	key := msg.Content.RelatesTo.Key
	if strings.HasPrefix(key, "mxc://") {
		emojiID, ok := dc.resolveCustomReactionEmoji(ctx, key)
		if !ok {
			return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("%w %s", errUnknownEmoji, key)
		}
		resp.EmojiID = emojiID
	} else {
		// Unicode emoji: dedup on the fully-qualified character.
		resp.Emoji = variationselector.FullyQualify(key)
	}
	return resp, nil
}

// resolveCustomReactionEmoji maps a custom-emoji mxc:// reaction key back to the
// Discord "<name>:<id>" form. It consults the dc_file cache (which records the
// emoji snowflake and name for previously bridged custom emoji), mirroring the
// legacy DB.File.GetEmojiByMXC path. Returns ("", false) if the mxc is unknown.
func (dc *DiscordClient) resolveCustomReactionEmoji(ctx context.Context, mxc string) (emojiID networkid.EmojiID, ok bool) {
	file, err := dc.connector.DB.File.GetByMXC(ctx, mxc)
	if err != nil {
		dc.logger().Warn().Err(err).Str("mxc", mxc).Msg("Failed to look up emoji by mxc for reaction")
		return "", false
	}
	if file == nil || file.ID == nil || *file.ID == "" || file.EmojiName == nil || *file.EmojiName == "" {
		return "", false
	}
	return networkid.EmojiID(fmt.Sprintf("%s:%s", *file.EmojiName, *file.ID)), true
}

// HandleMatrixReaction sends a reaction to Discord (FR-40). The framework
// supplies the resolved emoji in msg.PreHandleResp and the target message DB
// row; this method does the Discord API call and returns a partly-filled
// reaction row whose remaining fields the framework fills in.
func (dc *DiscordClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if !dc.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if msg.TargetMessage == nil {
		return nil, errTargetNotFound
	}
	sess := dc.Session
	if sess == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)

	reactChannelID, reactMsgID, ok := discordIDsForMessage(msg.TargetMessage)
	if !ok || reactMsgID == "" {
		return nil, fmt.Errorf("%w: unparseable target ID %q", errTargetNotFound, msg.TargetMessage.ID)
	}
	threadID := ""
	if tid := messageThread(msg.TargetMessage); tid != "" {
		threadID = tid
		reactChannelID = tid
	}

	emojiAPIArg := discordReactionAPIArg(msg.PreHandleResp)
	if emojiAPIArg == "" {
		return nil, fmt.Errorf("%w: empty resolved emoji", errUnknownEmoji)
	}

	if err := sess.MessageReactionAddUser(info.guildID, reactChannelID, reactMsgID, emojiAPIArg, dc.requestOpts(ctx, info, threadID)...); err != nil {
		return nil, fmt.Errorf("failed to add reaction on Discord: %w", err)
	}

	// Return a row carrying the resolved emoji; the framework fills Room,
	// MessageID, MXID, SenderID, SenderMXID and Timestamp.
	dbReaction := &database.Reaction{
		Metadata: &ReactionMeta{},
	}
	if msg.PreHandleResp != nil {
		dbReaction.EmojiID = msg.PreHandleResp.EmojiID
		dbReaction.Emoji = msg.PreHandleResp.Emoji
	}
	return dbReaction, nil
}

// discordReactionAPIArg returns the emoji argument the Discord reaction REST API
// expects: "name:id" for custom emoji (the EmojiID form) or the Unicode
// character for standard emoji.
func discordReactionAPIArg(pre *bridgev2.MatrixReactionPreResponse) string {
	if pre == nil {
		return ""
	}
	if pre.EmojiID != "" {
		return string(pre.EmojiID)
	}
	return pre.Emoji
}

// HandleMatrixReactionRemove removes a reaction from Discord (FR-40). The
// framework supplies the target reaction DB row.
func (dc *DiscordClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if !dc.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if msg.TargetReaction == nil {
		return errTargetNotFound
	}
	sess := dc.Session
	if sess == nil {
		return bridgev2.ErrNotLoggedIn
	}
	info := portalInfo(msg.Portal)

	reactChannelID, reactMsgID, ok := discordid.ParseMessageID(msg.TargetReaction.MessageID)
	if !ok || reactMsgID == "" {
		return fmt.Errorf("%w: unparseable reaction target %q", errTargetNotFound, msg.TargetReaction.MessageID)
	}

	emojiAPIArg := reactionRowAPIArg(msg.TargetReaction)
	if emojiAPIArg == "" {
		return fmt.Errorf("%w: reaction row has no emoji", errUnknownEmoji)
	}

	// Remove the reaction owned by its original sender.
	senderID := string(msg.TargetReaction.SenderID)
	if err := sess.MessageReactionRemoveUser(info.guildID, reactChannelID, reactMsgID, emojiAPIArg, senderID, dc.requestOpts(ctx, info, "")...); err != nil {
		return fmt.Errorf("failed to remove reaction on Discord: %w", err)
	}
	return nil
}

// reactionRowAPIArg returns the Discord reaction-API emoji argument for a stored
// reaction row: the EmojiID ("name:id") for custom emoji or the Unicode char.
func reactionRowAPIArg(r *database.Reaction) string {
	if r.EmojiID != "" {
		return string(r.EmojiID)
	}
	return r.Emoji
}

// --- ReadReceiptHandlingNetworkAPI (Task 5.3) ---

// HandleMatrixReadReceipt sends a ChannelMessageAck to Discord (FR-37, OQ-13).
// Only user tokens ack (bots have no read state). msg.ExactMessage may be nil —
// in that case there is no concrete Discord message to ack and the receipt is
// dropped. Thread-scoped receipts ack within the thread channel.
func (dc *DiscordClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if !dc.IsLoggedIn() {
		return nil
	}
	sess := dc.Session
	if sess == nil {
		return nil
	}
	// Only user accounts have a read state to update (FR-37); bots ack nothing.
	if !sess.IsUser {
		return nil
	}
	// ExactMessage may be nil (the read-up-to event isn't a bridged message).
	// Without a concrete Discord message there is nothing to ack.
	if msg.ExactMessage == nil {
		dc.logger().Debug().
			Stringer("event_id", msg.EventID).
			Msg("Dropping read receipt: no exact message")
		return nil
	}

	ackChannelID, ackMsgID, ok := discordIDsForMessage(msg.ExactMessage)
	if !ok || ackMsgID == "" {
		return nil
	}
	// Thread-scoped: ack inside the thread channel when the message lives in one.
	if tid := messageThread(msg.ExactMessage); tid != "" {
		ackChannelID = tid
	}

	resp, err := sess.ChannelMessageAckNoToken(ackChannelID, ackMsgID, discordgo.WithContext(ctx))
	if err != nil {
		dc.logger().Err(err).
			Str("channel_id", ackChannelID).
			Str("message_id", ackMsgID).
			Msg("Failed to send read receipt to Discord")
		return err
	}
	if resp != nil && resp.Token != nil {
		dc.logger().Debug().
			Str("unexpected_resp_token", *resp.Token).
			Msg("Marked message as read on Discord (and got unexpected non-nil token)")
	}
	return nil
}

// --- TypingHandlingNetworkAPI (Task 5.3) ---

// HandleMatrixTyping sends a typing indicator to Discord (FR-37). Discord typing
// is start-only (it auto-expires after ~10s), so a "stopped typing" event is a
// no-op.
func (dc *DiscordClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if !msg.IsTyping {
		return nil
	}
	if !dc.IsLoggedIn() {
		return nil
	}
	sess := dc.Session
	if sess == nil {
		return nil
	}
	info := portalInfo(msg.Portal)
	if err := sess.ChannelTyping(info.channelID, dc.requestOpts(ctx, info, "")...); err != nil {
		dc.logger().Warn().Err(err).
			Str("channel_id", info.channelID).
			Msg("Failed to mark as typing on Discord")
		return err
	}
	return nil
}

// --- RoomNameHandlingNetworkAPI (Task 5.3, ar H8: returns (bool, error)) ---

// HandleMatrixRoomName pushes a Matrix room name change to the Discord channel
// name when permitted. Returns (true, nil) when the change was applied (so the
// framework persists it), (false, nil) when it cannot/should not be pushed
// (DMs, missing session).
func (dc *DiscordClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if msg.Content == nil {
		return false, nil
	}
	info := portalInfo(msg.Portal)
	// DMs and group DMs don't have an editable channel name from this direction.
	if info.isDM {
		return false, nil
	}
	sess := dc.Session
	if sess == nil || !dc.IsLoggedIn() {
		return false, nil
	}
	_, err := sess.ChannelEdit(info.channelID, &discordgo.ChannelEdit{
		Name: msg.Content.Name,
	}, discordgo.WithContext(ctx))
	if err != nil {
		dc.logger().Warn().Err(err).
			Str("channel_id", info.channelID).
			Msg("Failed to update Discord channel name from Matrix")
		return false, err
	}
	return true, nil
}

// --- RoomTopicHandlingNetworkAPI (Task 5.3, ar H8: returns (bool, error)) ---

// HandleMatrixRoomTopic pushes a Matrix room topic change to the Discord channel
// topic when permitted. Returns (true, nil) on success, (false, nil) when it
// can't be pushed.
func (dc *DiscordClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if msg.Content == nil {
		return false, nil
	}
	info := portalInfo(msg.Portal)
	if info.isDM {
		return false, nil
	}
	sess := dc.Session
	if sess == nil || !dc.IsLoggedIn() {
		return false, nil
	}
	_, err := sess.ChannelEdit(info.channelID, &discordgo.ChannelEdit{
		Topic: msg.Content.Topic,
	}, discordgo.WithContext(ctx))
	if err != nil {
		dc.logger().Warn().Err(err).
			Str("channel_id", info.channelID).
			Msg("Failed to update Discord channel topic from Matrix")
		return false, err
	}
	return true, nil
}
