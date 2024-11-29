package main

import (
	"context"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Thread struct {
	*database.Thread
	Parent *Portal

	creationNoticeLock       sync.Mutex
	initialBackfillAttempted bool
}

func (br *DiscordBridge) GetThreadByID(id string, root *database.Message) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByID[id]
	if !ok {
		return br.loadThread(br.DB.Thread.GetByDiscordID(id), id, root)
	}
	return thread
}

func (br *DiscordBridge) GetThreadByRootMXID(mxid id.EventID) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByRootMXID[mxid]
	if !ok {
		return br.loadThread(br.DB.Thread.GetByMatrixRootMsg(mxid), "", nil)
	}
	return thread
}

func (br *DiscordBridge) GetThreadByRootOrCreationNoticeMXID(mxid id.EventID) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByRootMXID[mxid]
	if !ok {
		thread, ok = br.threadsByCreationNoticeMXID[mxid]
		if !ok {
			return br.loadThread(br.DB.Thread.GetByMatrixRootOrCreationNoticeMsg(mxid), "", nil)
		}
	}
	return thread
}

func (br *DiscordBridge) loadThread(dbThread *database.Thread, id string, root *database.Message) *Thread {
	if dbThread == nil {
		if root == nil {
			return nil
		}
		dbThread = br.DB.Thread.New()
		dbThread.ID = id
		dbThread.RootDiscordID = root.DiscordID
		dbThread.RootMXID = root.MXID
		dbThread.ParentID = root.Channel.ChannelID
		dbThread.Insert()
	}
	thread := &Thread{
		Thread: dbThread,
	}
	thread.Parent = br.GetExistingPortalByID(database.NewPortalKey(thread.ParentID, ""))
	br.threadsByID[thread.ID] = thread
	br.threadsByRootMXID[thread.RootMXID] = thread
	if thread.CreationNoticeMXID != "" {
		br.threadsByCreationNoticeMXID[thread.CreationNoticeMXID] = thread
	}
	return thread
}

func (br *DiscordBridge) threadFound(ctx context.Context, source *User, rootMessage *database.Message, id string, metadata *discordgo.Channel) {
	thread := br.GetThreadByID(id, rootMessage)
	log := zerolog.Ctx(ctx)
	log.Debug().Msg("Marked message as thread root")
	if thread.CreationNoticeMXID == "" {
		thread.Parent.sendThreadCreationNotice(ctx, thread)
	}
	// TODO member_ids_preview is probably not guaranteed to contain the source user
	if source != nil && metadata != nil && slices.Contains(metadata.MemberIDsPreview, source.DiscordID) && !source.IsInPortal(thread.ID) {
		source.MarkInPortal(database.UserPortal{
			DiscordID: thread.ID,
			Type:      database.UserPortalTypeThread,
			Timestamp: time.Now(),
		})
		if metadata.MessageCount > 0 {
			go thread.maybeInitialBackfill(source)
		} else {
			thread.initialBackfillAttempted = true
		}
	}
}

func (thread *Thread) maybeInitialBackfill(source *User) {
	if thread.initialBackfillAttempted || thread.Parent.bridge.Config.Bridge.Backfill.Limits.Initial.Thread == 0 {
		return
	}
	thread.Parent.forwardBackfillLock.Lock()
	if thread.Parent.bridge.DB.Message.GetLastInThread(thread.Parent.Key, thread.ID) != nil {
		thread.Parent.forwardBackfillLock.Unlock()
		return
	}
	thread.Parent.forwardBackfillInitial(source, thread)
}

func (thread *Thread) RefererOpt() discordgo.RequestOption {
	return discordgo.WithThreadReferer(thread.Parent.GuildID, thread.ParentID, thread.ID)
}

func (thread *Thread) Join(user *User) {
	if user.IsInPortal(thread.ID) {
		return
	}
	log := user.log.With().Str("thread_id", thread.ID).Str("channel_id", thread.ParentID).Logger()
	log.Debug().Msg("Joining thread")

	var doBackfill, backfillStarted bool
	if !thread.initialBackfillAttempted && thread.Parent.bridge.Config.Bridge.Backfill.Limits.Initial.Thread > 0 {
		thread.Parent.forwardBackfillLock.Lock()
		lastMessage := thread.Parent.bridge.DB.Message.GetLastInThread(thread.Parent.Key, thread.ID)
		if lastMessage != nil {
			thread.Parent.forwardBackfillLock.Unlock()
		} else {
			doBackfill = true
			defer func() {
				if !backfillStarted {
					thread.Parent.forwardBackfillLock.Unlock()
				}
			}()
		}
	}

	var err error
	if user.Session.IsUser {
		err = user.Session.ThreadJoin(thread.ID, discordgo.WithLocationParam(discordgo.ThreadJoinLocationContextMenu), thread.RefererOpt())
	} else {
		err = user.Session.ThreadJoin(thread.ID)
	}
	if err != nil {
		log.Error().Err(err).Msg("Error joining thread")
	} else {
		user.MarkInPortal(database.UserPortal{
			DiscordID: thread.ID,
			Type:      database.UserPortalTypeThread,
			Timestamp: time.Now(),
		})
		if doBackfill {
			go thread.Parent.forwardBackfillInitial(user, thread)
			backfillStarted = true
		}
	}
}
