// FetchMessages and BackfillMessage construction — Task 4.4.
//
// Implements BackfillingNetworkAPI (FetchMessages) and
// BackfillingNetworkAPIWithLimits (GetBackfillMaxBatchCount) on *DiscordClient.
//
// Design refs: plan.md §4.4, ar-report.md M5 (AllowSlowFetch), D4 (bounded
// fetch), FR-39 (thread backfill via ThreadRoot), FR-41 (per-type batch limits),
// FR-42 (missed/gap backfill via Forward), FR-43 (deterministic IDs via
// discordid).
package connector

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// messageFetchChunkSize is the maximum number of messages Discord returns in a
// single ChannelMessages call. Matches the legacy constant (50).
const messageFetchChunkSize = 50

// slowFetchThreshold is the number of messages above which we consider a fetch
// "slow" (requiring multiple API round-trips). When AllowSlowFetch is false and
// the requested Count exceeds this, we return MoreRequiresSlowFetch instead of
// making the extra calls (ar M5).
const slowFetchThreshold = messageFetchChunkSize

// GetBackfillMaxBatchCount returns the connector-configured maximum number of
// backwards-backfill batches for the given portal type, reading per-type limits
// from config (FR-41).
//
// Return values < 0 are treated as unlimited by the framework.
func (dc *DiscordClient) GetBackfillMaxBatchCount(ctx context.Context, portal *bridgev2.Portal, task *database.BackfillTask) int {
	cfg := &dc.connector.Config.Backfill
	switch portal.RoomType {
	case database.RoomTypeDM, database.RoomTypeGroupDM:
		return cfg.Initial.DM
	default:
		// Threads are identified by a ThreadRoot on the backfill task; fall
		// through to channel limit for everything else.
		return cfg.Initial.Channel
	}
}

// FetchMessages fetches a batch of Discord messages for backfill. It is called
// by the framework for both initial/backward backfill (Forward=false) and
// missed/gap backfill (Forward=true).
//
// Thread backfill (FR-39): when params.ThreadRoot is non-empty, the Discord
// thread channel ID is extracted and used as the fetch target. The framework
// calls FetchMessages(parentPortal, ThreadRoot=…) for thread backfill, and the
// connector must read the thread channel from the message metadata or decode it
// from the ThreadRoot MessageID.
//
// AllowSlowFetch (ar M5): if the requested Count exceeds a single API page and
// AllowSlowFetch is false, this method returns MoreRequiresSlowFetch=true
// immediately rather than making multiple API calls.
//
// Bounded fetch (ar D4): never accumulates messages unboundedly; always
// respects params.Count; returns HasMore when more messages exist.
func (dc *DiscordClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	log := zerolog.Ctx(ctx)

	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("not connected")
	}

	portal := params.Portal
	portalChannelID := discordid.ParsePortalID(portal.ID)

	// Resolve the target channel: either the thread channel (from ThreadRoot)
	// or the portal channel itself. The ThreadRoot MessageID has the format
	// "channelID-messageID" (per discordid.MakeMessageID); the channel part is
	// the thread channel ID.
	targetChannelID := portalChannelID
	threadChannelID := "" // non-empty only when fetching inside a thread
	if params.ThreadRoot != "" {
		chanID, _, ok := discordid.ParseMessageID(params.ThreadRoot)
		if ok && chanID != portalChannelID {
			targetChannelID = chanID
			threadChannelID = chanID
		}
	}

	// Resolve the anchor Discord message ID (before/after cursor).
	var anchorID string
	if params.AnchorMessage != nil {
		_, msgID, ok := discordid.ParseMessageID(params.AnchorMessage.ID)
		if ok {
			anchorID = msgID
		}
	}
	// The string cursor (returned by a previous FetchMessages call) overrides
	// the anchor for backward paging.
	if params.Cursor != "" && !params.Forward {
		anchorID = string(params.Cursor)
	}

	count := params.Count
	if count <= 0 {
		count = messageFetchChunkSize
	}

	// Slow-fetch guard (ar M5): if the caller does not allow slow fetches and
	// we would need more than one API call, bail out early.
	if !params.AllowSlowFetch && count > slowFetchThreshold {
		log.Debug().
			Int("count", count).
			Int("threshold", slowFetchThreshold).
			Msg("AllowSlowFetch=false and count > threshold; returning MoreRequiresSlowFetch")
		return &bridgev2.FetchMessagesResponse{
			MoreRequiresSlowFetch: true,
		}, nil
	}

	// Build referer option for user tokens (bot tokens don't need it).
	refererOpts := dc.refererOptsForChannel(sess, portal, portalChannelID, threadChannelID)

	var rawMsgs []*discordgo.Message
	var err error

	if params.Forward {
		// Missed/gap backfill: fetch messages AFTER the anchor (newest-first
		// slice reversed to chronological order below).
		rawMsgs, err = dc.fetchForward(ctx, sess, targetChannelID, anchorID, count, refererOpts)
	} else {
		// Initial/backward backfill: fetch messages BEFORE the anchor.
		rawMsgs, err = dc.fetchBackward(ctx, sess, targetChannelID, anchorID, count, refererOpts)
	}
	if err != nil {
		return nil, fmt.Errorf("fetching messages from Discord channel %s: %w", targetChannelID, err)
	}

	// Discord returns messages newest-first; sort into chronological order.
	sort.Slice(rawMsgs, func(i, j int) bool {
		return discordid.SnowflakeToTime(rawMsgs[i].ID).Before(discordid.SnowflakeToTime(rawMsgs[j].ID))
	})

	hasMore := len(rawMsgs) >= count

	// Trim to exactly count if we fetched more (shouldn't happen but guard it).
	if len(rawMsgs) > count {
		rawMsgs = rawMsgs[:count]
		hasMore = true
	}

	// Build the next backward-paging cursor: the Discord ID of the oldest
	// message in this batch (so the next call fetches earlier messages).
	var cursor networkid.PaginationCursor
	if hasMore && len(rawMsgs) > 0 {
		cursor = networkid.PaginationCursor(rawMsgs[0].ID)
	}

	msgs := make([]*bridgev2.BackfillMessage, 0, len(rawMsgs))
	for _, raw := range rawMsgs {
		bm, convErr := dc.convertToBackfillMessage(ctx, portal, raw, targetChannelID, threadChannelID)
		if convErr != nil {
			log.Warn().Err(convErr).Str("message_id", raw.ID).Msg("Skipping unconvertible message during backfill")
			continue
		}
		msgs = append(msgs, bm)
	}

	return &bridgev2.FetchMessagesResponse{
		Messages: msgs,
		HasMore:  hasMore,
		Cursor:   cursor,
		Forward:  params.Forward,
	}, nil
}

// fetchBackward fetches up to `limit` messages from Discord ending BEFORE
// `beforeID` (oldest-first ordering from Discord is newest-first; the caller
// sorts). Uses at most one API call (bounded by D4).
func (dc *DiscordClient) fetchBackward(
	ctx context.Context,
	sess *discordgo.Session,
	channelID, beforeID string,
	limit int,
	opts []discordgo.RequestOption,
) ([]*discordgo.Message, error) {
	if limit > messageFetchChunkSize {
		limit = messageFetchChunkSize
	}
	msgs, err := sess.ChannelMessages(channelID, limit, beforeID, "", "", opts...)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// fetchForward fetches up to `limit` messages from Discord starting AFTER
// `afterID`. A single API call is used (bounded fetch, ar D4).
func (dc *DiscordClient) fetchForward(
	ctx context.Context,
	sess *discordgo.Session,
	channelID, afterID string,
	limit int,
	opts []discordgo.RequestOption,
) ([]*discordgo.Message, error) {
	if limit > messageFetchChunkSize {
		limit = messageFetchChunkSize
	}
	msgs, err := sess.ChannelMessages(channelID, limit, "", afterID, "", opts...)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// refererOptsForChannel returns the appropriate Referer request option for
// user-account sessions. Bot tokens don't require or send a Referer header.
func (dc *DiscordClient) refererOptsForChannel(sess *discordgo.Session, portal *bridgev2.Portal, portalChannelID, threadChannelID string) []discordgo.RequestOption {
	if sess == nil || !sess.IsUser {
		return nil
	}
	// Resolve the guild ID from the portal metadata.
	var guildID string
	if portal != nil {
		if meta, ok := portal.Metadata.(*PortalMeta); ok && meta != nil {
			guildID = meta.GuildID
		}
	}
	if threadChannelID != "" {
		return []discordgo.RequestOption{discordgo.WithThreadReferer(guildID, portalChannelID, threadChannelID)}
	}
	return []discordgo.RequestOption{discordgo.WithChannelReferer(guildID, portalChannelID)}
}

// convertToBackfillMessage converts a raw discordgo.Message into a
// bridgev2.BackfillMessage. It assigns stable deterministic IDs via the
// discordid codec (FR-43) and builds BackfillReactions from the message's
// reaction summary.
//
// The actual message content conversion (formatter, embeds, attachments) is
// deferred to the convertdiscord converter; until that is implemented (Group
// 4.2), a minimal ConvertedMessage placeholder is produced.
func (dc *DiscordClient) convertToBackfillMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	raw *discordgo.Message,
	targetChannelID, threadChannelID string,
) (*bridgev2.BackfillMessage, error) {
	ts, err := discordgo.SnowflakeTimestamp(raw.ID)
	if err != nil {
		// Fall back to approximation from the snowflake.
		ts = discordid.SnowflakeToTime(raw.ID)
	}

	msgID := discordid.MakeMessageID(targetChannelID, raw.ID)

	// Build ConvertedMessage parts. Full conversion (attachments, embeds,
	// stickers, formatting) is implemented in Task 4.2 (convertdiscord.go).
	// At this stage produce a single text part so the framework can store and
	// index the message while the converter stub is replaced.
	converted := dc.convertDiscordMessageForBackfill(ctx, portal, raw, targetChannelID)

	// Determine the sender.
	var senderID string
	if raw.Author != nil {
		senderID = raw.Author.ID
	}
	sender := bridgev2.EventSender{
		Sender: discordid.MakeUserID(senderID),
	}
	if senderID != "" && dc.userLogin != nil {
		sender.IsFromMe = string(dc.userLogin.ID) == senderID
		if sender.IsFromMe {
			sender.SenderLogin = dc.userLogin.ID
		}
	}

	reactions := dc.convertBackfillReactions(raw, ts)

	bm := &bridgev2.BackfillMessage{
		ConvertedMessage: converted,
		Sender:           sender,
		ID:               msgID,
		Timestamp:        ts,
		StreamOrder:      ts.UnixMilli(),
		Reactions:        reactions,
	}

	// ShouldBackfillThread: set on thread-root messages so the framework
	// triggers doThreadBackfill for the child thread (FR-39, H7 correction).
	if raw.Thread != nil {
		bm.ShouldBackfillThread = true
	}

	return bm, nil
}

// convertDiscordMessageForBackfill produces a ConvertedMessage for a backfill
// batch. Full conversion is implemented in Task 4.2; this function provides the
// minimal wiring (message ID parts, thread root linkage) so the structure is
// correct even before convertdiscord.go is filled in.
func (dc *DiscordClient) convertDiscordMessageForBackfill(
	ctx context.Context,
	portal *bridgev2.Portal,
	raw *discordgo.Message,
	channelID string,
) *bridgev2.ConvertedMessage {
	// Build parts with deterministic PartIDs (FR-43).
	// Part 0 (text / primary): always empty PartID ("").
	// Parts 1..N (attachments): "attachment-<index>-<attachmentID>" sorted by
	// attachment ID ascending, matching the migration SQL window function (C2).
	parts := dc.buildBackfillParts(raw, channelID)

	converted := &bridgev2.ConvertedMessage{
		Parts: parts,
	}

	// Thread root linkage: if this message is inside a thread, set ThreadRoot
	// to the parent channel's thread-root message ID so the framework places
	// the event in the correct Matrix thread relation.
	if raw.MessageReference != nil && raw.MessageReference.ChannelID != "" && raw.MessageReference.MessageID != "" {
		rootID := discordid.MakeMessageID(raw.MessageReference.ChannelID, raw.MessageReference.MessageID)
		converted.ThreadRoot = &rootID
	}

	// Reply linkage.
	if raw.MessageReference != nil && raw.MessageReference.MessageID != "" && converted.ThreadRoot == nil {
		replyID := discordid.MakeMessageID(raw.MessageReference.ChannelID, raw.MessageReference.MessageID)
		converted.ReplyTo = &networkid.MessageOptionalPartID{MessageID: replyID}
	}

	return converted
}

// buildBackfillParts constructs ConvertedMessagePart slice with deterministic
// PartIDs matching the migration SQL convention (FR-43, C2).
func (dc *DiscordClient) buildBackfillParts(raw *discordgo.Message, channelID string) []*bridgev2.ConvertedMessagePart {
	// Sort attachments by ID ascending to match the SQL window function order.
	attachments := make([]*discordgo.MessageAttachment, len(raw.Attachments))
	copy(attachments, raw.Attachments)
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].ID < attachments[j].ID
	})

	// Determine how many parts we need.
	// Single-part: no attachments OR one attachment with caption that can be
	// merged → single part with PartID "".
	// Multi-part: text + each attachment → PartID "" + "attachment-N-id".
	if len(attachments) == 0 {
		// Text-only message.
		meta := &MessageMeta{DiscordID: raw.ID}
		return []*bridgev2.ConvertedMessagePart{
			{
				ID:         "",
				DBMetadata: meta,
			},
		}
	}

	var parts []*bridgev2.ConvertedMessagePart

	// First part: text content (PartID = "").
	textMeta := &MessageMeta{DiscordID: raw.ID}
	textPart := &bridgev2.ConvertedMessagePart{
		ID:         "",
		DBMetadata: textMeta,
	}
	parts = append(parts, textPart)

	// Attachment parts.
	for i, att := range attachments {
		partID := discordid.MakePartID(i, att.ID)
		meta := &MessageMeta{DiscordID: raw.ID}
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			ID:         partID,
			DBMetadata: meta,
		})
	}

	return parts
}

// convertBackfillReactions converts the reaction summary embedded in a Discord
// message into BackfillReaction slice. Discord's ChannelMessages response
// includes aggregate reaction counts and emoji but not individual reactor IDs;
// the framework stores reactions using the bridge bot's identity when the
// sender is unknown.
func (dc *DiscordClient) convertBackfillReactions(raw *discordgo.Message, msgTS time.Time) []*bridgev2.BackfillReaction {
	if len(raw.Reactions) == 0 {
		return nil
	}
	reactions := make([]*bridgev2.BackfillReaction, 0, len(raw.Reactions))
	for _, r := range raw.Reactions {
		if r.Emoji == nil {
			continue
		}
		var emojiID networkid.EmojiID
		var emojiStr string
		if r.Emoji.ID != "" {
			// Custom emoji: encode as "<name>:<id>" matching the reaction
			// emoji split convention (ar M10).
			emojiID = networkid.EmojiID(r.Emoji.ID)
			emojiStr = fmt.Sprintf(":%s:", r.Emoji.Name)
		} else {
			// Unicode emoji: both emojiID and emoji are the character.
			emojiID = networkid.EmojiID(r.Emoji.Name)
			emojiStr = r.Emoji.Name
		}

		// Use a timestamp slightly after the message so reactions sort after
		// the message itself. The per-reactor timestamp is not available from
		// the summary endpoint (ar M10).
		reactionTS := msgTS.Add(time.Millisecond)

		reactions = append(reactions, &bridgev2.BackfillReaction{
			Timestamp: reactionTS,
			// Sender is left as the zero EventSender; the framework will use
			// the bridge bot identity when SenderLogin and Sender are empty.
			EmojiID: emojiID,
			Emoji:   emojiStr,
		})
	}
	return reactions
}
