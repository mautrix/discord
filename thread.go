package main

import (
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Thread struct {
	*database.Thread
	Parent *Portal
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
	return thread
}
