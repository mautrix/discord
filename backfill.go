package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

func (portal *Portal) forwardBackfillInitial(source *User, thread *Thread) {
	log := portal.log
	defer func() {
		log.Debug().Msg("Forward backfill finished, unlocking lock")
		portal.forwardBackfillLock.Unlock()
	}()
	// This should only be called from CreateMatrixRoom which locks forwardBackfillLock before creating the room.
	if portal.forwardBackfillLock.TryLock() {
		panic("forwardBackfillInitial() called without locking forwardBackfillLock")
	}

	limit := portal.bridge.Config.Bridge.Backfill.Limits.Initial.Channel
	if portal.GuildID == "" {
		limit = portal.bridge.Config.Bridge.Backfill.Limits.Initial.DM
		if thread != nil {
			limit = portal.bridge.Config.Bridge.Backfill.Limits.Initial.Thread
			thread.initialBackfillAttempted = true
		}
	}
	if limit == 0 {
		return
	}

	with := log.With().
		Str("action", "initial backfill").
		Str("room_id", portal.MXID.String()).
		Int("limit", limit)
	if thread != nil {
		with = with.Str("thread_id", thread.ID)
	}
	log = with.Logger()

	portal.backfillLimited(log, source, limit, "", thread)
}

func (portal *Portal) ForwardBackfillMissed(source *User, serverLastMessageID string, thread *Thread) {
	if portal.MXID == "" {
		return
	}

	limit := portal.bridge.Config.Bridge.Backfill.Limits.Missed.Channel
	if portal.GuildID == "" {
		limit = portal.bridge.Config.Bridge.Backfill.Limits.Missed.DM
		if thread != nil {
			limit = portal.bridge.Config.Bridge.Backfill.Limits.Missed.Thread
		}
	}
	if limit == 0 {
		return
	}
	with := portal.log.With().
		Str("action", "missed event backfill").
		Str("room_id", portal.MXID.String()).
		Int("limit", limit)
	if thread != nil {
		with = with.Str("thread_id", thread.ID)
	}
	log := with.Logger()

	portal.forwardBackfillLock.Lock()
	defer portal.forwardBackfillLock.Unlock()

	var lastMessage *database.Message
	if thread != nil {
		lastMessage = portal.bridge.DB.Message.GetLastInThread(portal.Key, thread.ID)
	} else {
		lastMessage = portal.bridge.DB.Message.GetLast(portal.Key)
	}
	if lastMessage == nil || serverLastMessageID == "" {
		log.Debug().Msg("Not backfilling, no last message in database or no last message in metadata")
		return
	} else if !shouldBackfill(lastMessage.DiscordID, serverLastMessageID) {
		log.Debug().
			Str("last_bridged_message", lastMessage.DiscordID).
			Str("last_server_message", serverLastMessageID).
			Msg("Not backfilling, last message in database is newer than last message in metadata")
		return
	}
	log.Debug().
		Str("last_bridged_message", lastMessage.DiscordID).
		Str("last_server_message", serverLastMessageID).
		Msg("Backfilling missed messages")
	if limit < 0 {
		portal.backfillUnlimitedMissed(log, source, lastMessage.DiscordID, thread)
	} else {
		portal.backfillLimited(log, source, limit, lastMessage.DiscordID, thread)
	}
}

const messageFetchChunkSize = 50

func (portal *Portal) collectBackfillMessages(log zerolog.Logger, source *User, limit int, until string, thread *Thread) ([]*discordgo.Message, bool, error) {
	var messages []*discordgo.Message
	var before string
	var foundAll bool
	protoChannelID := portal.Key.ChannelID
	if thread != nil {
		protoChannelID = thread.ID
	}
	for {
		log.Debug().Str("before_id", before).Msg("Fetching messages for backfill")
		newMessages, err := source.Session.ChannelMessages(protoChannelID, messageFetchChunkSize, before, "", "", portal.RefererOptIfUser(source.Session, protoChannelID)...)
		if err != nil {
			return nil, false, err
		}
		if until != "" {
			for i, msg := range newMessages {
				if compareMessageIDs(msg.ID, until) <= 0 {
					log.Debug().
						Str("message_id", msg.ID).
						Str("until_id", until).
						Msg("Found message that was already bridged")
					newMessages = newMessages[:i]
					foundAll = true
					break
				}
			}
		}
		messages = append(messages, newMessages...)
		log.Debug().Int("count", len(newMessages)).Msg("Added messages to backfill collection")
		if len(newMessages) < messageFetchChunkSize || len(messages) >= limit {
			break
		}
		before = newMessages[len(newMessages)-1].ID
	}
	if len(messages) > limit {
		foundAll = false
		messages = messages[:limit]
	}
	return messages, foundAll, nil
}

func (portal *Portal) backfillLimited(log zerolog.Logger, source *User, limit int, after string, thread *Thread) {
	messages, foundAll, err := portal.collectBackfillMessages(log, source, limit, after, thread)
	if err != nil {
		if source.handlePossible40002(err) {
			panic(err)
		}
		log.Err(err).Msg("Error collecting messages to forward backfill")
		return
	}
	log.Info().
		Int("count", len(messages)).
		Bool("found_all", foundAll).
		Msg("Collected messages to backfill")
	sort.Sort(MessageSlice(messages))
	if !foundAll && after != "" {
		_, err = portal.sendMatrixMessage(portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "Some messages may have been missed here while the bridge was offline.",
		}, nil, 0)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send missed message warning")
		} else {
			log.Debug().Msg("Sent warning about possibly missed messages")
		}
	}
	portal.sendBackfillBatch(log, source, messages, thread)
}

func (portal *Portal) backfillUnlimitedMissed(log zerolog.Logger, source *User, after string, thread *Thread) {
	protoChannelID := portal.Key.ChannelID
	if thread != nil {
		protoChannelID = thread.ID
	}
	for {
		log.Debug().Str("after_id", after).Msg("Fetching chunk of messages to backfill")
		messages, err := source.Session.ChannelMessages(protoChannelID, messageFetchChunkSize, "", after, "", portal.RefererOptIfUser(source.Session, protoChannelID)...)
		if err != nil {
			log.Err(err).Msg("Error fetching chunk of messages to forward backfill")
			return
		}
		log.Debug().Int("count", len(messages)).Msg("Fetched chunk of messages to backfill")
		sort.Sort(MessageSlice(messages))

		portal.sendBackfillBatch(log, source, messages, thread)

		if len(messages) < messageFetchChunkSize {
			// Assume that was all the missing messages
			log.Debug().Msg("Chunk had less than 50 messages, stopping backfill")
			return
		}
		after = messages[len(messages)-1].ID
	}
}

func (portal *Portal) sendBackfillBatch(log zerolog.Logger, source *User, messages []*discordgo.Message, thread *Thread) {
	if portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureBatchSending) {
		log.Debug().Msg("Using hungryserv, sending messages with batch send endpoint")
		portal.forwardBatchSend(log, source, messages, thread)
	} else {
		log.Debug().Msg("Not using hungryserv, sending messages one by one")
		for _, msg := range messages {
			portal.handleDiscordMessageCreate(source, msg, thread)
		}
	}
}

func (portal *Portal) forwardBatchSend(log zerolog.Logger, source *User, messages []*discordgo.Message, thread *Thread) {
	evts, metas, dbMessages := portal.convertMessageBatch(log, source, messages, thread)
	if len(evts) == 0 {
		log.Warn().Msg("Didn't get any events to backfill")
		return
	}
	log.Info().Int("events", len(evts)).Msg("Converted messages to backfill")
	resp, err := portal.MainIntent().BeeperBatchSend(portal.MXID, &mautrix.ReqBeeperBatchSend{
		Forward: true,
		Events:  evts,
	})
	if err != nil {
		log.Err(err).Msg("Error sending backfill batch")
		return
	}
	for i, evtID := range resp.EventIDs {
		dbMessages[i].MXID = evtID
		if metas[i] != nil && metas[i].Flags == discordgo.MessageFlagsHasThread {
			// TODO proper context
			ctx := log.WithContext(context.Background())
			portal.bridge.threadFound(ctx, source, &dbMessages[i], metas[i].ID, metas[i].Thread)
		}
	}
	portal.bridge.DB.Message.MassInsert(portal.Key, dbMessages)
}

func (portal *Portal) convertMessageBatch(log zerolog.Logger, source *User, messages []*discordgo.Message, thread *Thread) ([]*event.Event, []*discordgo.Message, []database.Message) {
	var discordThreadID string
	var threadRootEvent, lastThreadEvent id.EventID
	if thread != nil {
		discordThreadID = thread.ID
		threadRootEvent = thread.RootMXID
		lastThreadEvent = threadRootEvent
		lastInThread := portal.bridge.DB.Message.GetLastInThread(portal.Key, thread.ID)
		if lastInThread != nil {
			lastThreadEvent = lastInThread.MXID
		}
	}

	evts := make([]*event.Event, 0, len(messages))
	dbMessages := make([]database.Message, 0, len(messages))
	metas := make([]*discordgo.Message, 0, len(messages))
	ctx := context.Background()
	for _, msg := range messages {
		for _, mention := range msg.Mentions {
			puppet := portal.bridge.GetPuppetByID(mention.ID)
			puppet.UpdateInfo(nil, mention, nil)
		}

		puppet := portal.bridge.GetPuppetByID(msg.Author.ID)
		puppet.UpdateInfo(source, msg.Author, msg)
		intent := puppet.IntentFor(portal)
		replyTo := portal.getReplyTarget(source, discordThreadID, msg.MessageReference, msg.Embeds, true)
		mentions := portal.convertDiscordMentions(msg, false)

		ts, _ := discordgo.SnowflakeTimestamp(msg.ID)
		log := log.With().
			Str("message_id", msg.ID).
			Int("message_type", int(msg.Type)).
			Str("author_id", msg.Author.ID).
			Logger()
		parts := portal.convertDiscordMessage(log.WithContext(ctx), puppet, intent, msg)
		for i, part := range parts {
			if (replyTo != nil || threadRootEvent != "") && part.Content.RelatesTo == nil {
				part.Content.RelatesTo = &event.RelatesTo{}
			}
			if threadRootEvent != "" {
				part.Content.RelatesTo.SetThread(threadRootEvent, lastThreadEvent)
			}
			if replyTo != nil {
				part.Content.RelatesTo.SetReplyTo(replyTo.EventID)
				// Only set reply for first event
				replyTo = nil
			}

			part.Content.Mentions = mentions
			// Only set mentions for first event, but keep empty object for rest
			mentions = &event.Mentions{}

			partName := part.AttachmentID
			// Always use blank part name for first part so that replies and other things
			// can reference it without knowing about attachments.
			if i == 0 {
				partName = ""
			}
			evt := &event.Event{
				ID:        portal.deterministicEventID(msg.ID, partName),
				Type:      part.Type,
				Sender:    intent.UserID,
				Timestamp: ts.UnixMilli(),
				Content: event.Content{
					Parsed: part.Content,
					Raw:    part.Extra,
				},
			}
			var err error
			evt.Type, err = portal.encrypt(intent, &evt.Content, evt.Type)
			if err != nil {
				log.Err(err).Msg("Failed to encrypt event")
				continue
			}
			intent.AddDoublePuppetValue(&evt.Content)
			evts = append(evts, evt)
			dbMessages = append(dbMessages, database.Message{
				Channel:      portal.Key,
				DiscordID:    msg.ID,
				SenderID:     msg.Author.ID,
				Timestamp:    ts,
				AttachmentID: part.AttachmentID,
				SenderMXID:   intent.UserID,
			})
			if i == 0 {
				metas = append(metas, msg)
			} else {
				metas = append(metas, nil)
			}
			lastThreadEvent = evt.ID
		}
	}
	return evts, metas, dbMessages
}

func (portal *Portal) deterministicEventID(messageID, partName string) id.EventID {
	data := fmt.Sprintf("%s/discord/%s/%s", portal.MXID, messageID, partName)
	sum := sha256.Sum256([]byte(data))
	return id.EventID(fmt.Sprintf("$%s:discord.com", base64.RawURLEncoding.EncodeToString(sum[:])))
}

// compareMessageIDs compares two Discord message IDs.
//
// If the first ID is lower, -1 is returned.
// If the second ID is lower, 1 is returned.
// If the IDs are equal, 0 is returned.
func compareMessageIDs(id1, id2 string) int {
	if id1 == id2 {
		return 0
	}
	if len(id1) < len(id2) {
		return -1
	} else if len(id2) < len(id1) {
		return 1
	}
	if id1 < id2 {
		return -1
	}
	return 1
}

func shouldBackfill(latestBridgedIDStr, latestIDFromServerStr string) bool {
	return compareMessageIDs(latestBridgedIDStr, latestIDFromServerStr) == -1
}

type MessageSlice []*discordgo.Message

var _ sort.Interface = (MessageSlice)(nil)

func (a MessageSlice) Len() int {
	return len(a)
}

func (a MessageSlice) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a MessageSlice) Less(i, j int) bool {
	return compareMessageIDs(a[i].ID, a[j].ID) == -1
}
