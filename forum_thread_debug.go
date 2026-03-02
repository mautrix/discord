package main

import (
	"fmt"
	"os"
	"strings"

	"maunium.net/go/mautrix/event"
)

const forumThreadDebugEnv = "MAUTRIX_DISCORD_FORUM_THREAD_DEBUG"

func forumThreadDebugEnabled() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(forumThreadDebugEnv)))
	switch val {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func forumThreadDebugReply(ce *WrappedCommandEvent, format string, args ...interface{}) {
	if !forumThreadDebugEnabled() {
		return
	}
	ce.Reply("[forum-thread-debug] "+format, args...)
}

func (user *User) forumThreadDebugNotice(format string, args ...interface{}) {
	if !forumThreadDebugEnabled() || user == nil || user.bridge == nil || user.bridge.Bot == nil {
		return
	}
	roomID := user.GetManagementRoomID()
	if roomID == "" {
		return
	}
	_, err := user.bridge.Bot.SendMessageEvent(roomID, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("[forum-thread-debug] "+format, args...),
	})
	if err != nil {
		user.log.Warn().Err(err).Str("room_id", roomID.String()).Msg("Failed to send forum-thread debug notice")
	}
}
