package main

import (
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Thread struct {
	*database.Thread
	Parent *Portal

	creationNoticeLock sync.Mutex
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

func (thread *Thread) Join(user *User) {
	if user.IsInPortal(thread.ID) {
		return
	}
	log := user.log.With().Str("thread_id", thread.ID).Str("channel_id", thread.ParentID).Logger()
	log.Debug().Msg("Joining thread")
	var err error
	if user.Session.IsUser {
		err = user.Session.ThreadJoinWithLocation(thread.ID, discordgo.ThreadJoinLocationContextMenu)
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
	}
}
