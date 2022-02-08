package bridge

import (
	"strings"

	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

type matrixHandler struct {
	as     *appservice.AppService
	bridge *Bridge
	log    maulogger.Logger
	cmd    *commandHandler
}

func (b *Bridge) setupEvents() {
	b.eventProcessor = appservice.NewEventProcessor(b.as)

	b.matrixHandler = &matrixHandler{
		as:     b.as,
		bridge: b,
		log:    b.log.Sub("Matrix"),
		cmd:    newCommandHandler(b),
	}

	b.eventProcessor.On(event.EventMessage, b.matrixHandler.handleMessage)
	b.eventProcessor.On(event.EventReaction, b.matrixHandler.handleReaction)
	b.eventProcessor.On(event.EventRedaction, b.matrixHandler.handleRedaction)
	b.eventProcessor.On(event.StateMember, b.matrixHandler.handleMembership)
}

func (mh *matrixHandler) join(evt *event.Event, intent *appservice.IntentAPI) *mautrix.RespJoinedMembers {
	resp, err := intent.JoinRoomByID(evt.RoomID)
	if err != nil {
		mh.log.Debugfln("Failed to join room %s as %s with invite from %s: %v", evt.RoomID, intent.UserID, evt.Sender, err)

		return nil
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		intent.LeaveRoom(resp.RoomID)

		mh.log.Debugfln("Failed to get members in room %s after accepting invite from %s as %s: %v", resp.RoomID, evt.Sender, intent.UserID, err)

		return nil
	}

	if len(members.Joined) < 2 {
		intent.LeaveRoom(resp.RoomID)

		mh.log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender, "as", intent.UserID)

		return nil
	}

	return members
}

func (mh *matrixHandler) ignoreEvent(evt *event.Event) bool {
	return false
}

func (mh *matrixHandler) handleMessage(evt *event.Event) {
	if mh.ignoreEvent(evt) {
		return
	}

	user := mh.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		mh.log.Debugln("unknown user", evt.Sender)
		return
	}

	content := evt.Content.AsMessage()
	content.RemoveReplyFallback()

	if content.MsgType == event.MsgText {
		prefix := mh.bridge.Config.Bridge.CommandPrefix

		hasPrefix := strings.HasPrefix(content.Body, prefix)
		if hasPrefix {
			content.Body = strings.TrimLeft(content.Body[len(prefix):], " ")
		}

		if hasPrefix || evt.RoomID == user.ManagementRoom {
			mh.cmd.handle(evt.RoomID, user, content.Body, content.GetReplyTo())
			return
		}
	}

	portal := mh.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil {
		portal.matrixMessages <- portalMatrixMessage{user: user, evt: evt}
	}

}

func (mh *matrixHandler) joinAndCheckMembers(evt *event.Event, intent *appservice.IntentAPI) *mautrix.RespJoinedMembers {
	resp, err := intent.JoinRoomByID(evt.RoomID)
	if err != nil {
		mh.log.Debugfln("Failed to join room %q as %q with invite from %q: %v", evt.RoomID, intent.UserID, evt.Sender, err)

		return nil
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		mh.log.Debugfln("Failed to get members in room %q with invite from %q as %q: %v", resp.RoomID, evt.Sender, intent.UserID, err)

		return nil
	}

	if len(members.Joined) < 2 {
		mh.log.Debugfln("Leaving empty room %q with invite from %q as %q", resp.RoomID, evt.Sender, intent.UserID)

		intent.LeaveRoom(resp.RoomID)

		return nil
	}

	return members
}

func (mh *matrixHandler) sendNoticeWithmarkdown(roomID id.RoomID, message string) (*mautrix.RespSendEvent, error) {
	intent := mh.as.BotIntent()
	content := format.RenderMarkdown(message, true, false)
	content.MsgType = event.MsgNotice

	return intent.SendMessageEvent(roomID, event.EventMessage, content)
}

func (mh *matrixHandler) handleBotInvite(evt *event.Event) {
	intent := mh.as.BotIntent()

	user := mh.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	members := mh.joinAndCheckMembers(evt, intent)
	if members == nil {
		return
	}

	// If this is a DM and the user doesn't have a management room, make this
	// the management room.
	if len(members.Joined) == 2 && (user.ManagementRoom == "" || evt.Content.AsMember().IsDirect) {
		user.SetManagementRoom(evt.RoomID)

		intent.SendNotice(user.ManagementRoom, "This room has been registered as your bridge management/status room")
		mh.log.Debugfln("%q registered as management room with %q", evt.RoomID, evt.Sender)
	}

	// Wait to send the welcome message until we're sure we're not in an empty
	// room.
	mh.sendNoticeWithmarkdown(evt.RoomID, mh.bridge.Config.Bridge.ManagementRoomText.Welcome)

	if evt.RoomID == user.ManagementRoom {
		if user.HasSession() {
			mh.sendNoticeWithmarkdown(evt.RoomID, mh.bridge.Config.Bridge.ManagementRoomText.Connected)
		} else {
			mh.sendNoticeWithmarkdown(evt.RoomID, mh.bridge.Config.Bridge.ManagementRoomText.NotConnected)
		}

		additionalHelp := mh.bridge.Config.Bridge.ManagementRoomText.AdditionalHelp
		if additionalHelp != "" {
			mh.sendNoticeWithmarkdown(evt.RoomID, additionalHelp)
		}
	}
}

func (mh *matrixHandler) handlePuppetInvite(evt *event.Event, inviter *User, puppet *Puppet) {
	mh.log.Warnln("handling puppet invite!")
}

func (mh *matrixHandler) handleMembership(evt *event.Event) {
	// Return early if we're supposed to ignore the event.
	if mh.ignoreEvent(evt) {
		return
	}

	// Grab the content of the event.
	content := evt.Content.AsMember()

	// Check if this is a new conversation from a matrix user to the bot
	if content.Membership == event.MembershipInvite && id.UserID(evt.GetStateKey()) == mh.as.BotMXID() {
		mh.handleBotInvite(evt)

		return
	}

	// Load or create a new user.
	user := mh.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	puppet := mh.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))

	// Load or create a new portal.
	portal := mh.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		if content.Membership == event.MembershipInvite && puppet != nil {
			mh.handlePuppetInvite(evt, user, puppet)
		}

		return
	}

	isSelf := id.UserID(evt.GetStateKey()) == evt.Sender

	if content.Membership == event.MembershipLeave {
		if evt.Unsigned.PrevContent != nil {
			_ = evt.Unsigned.PrevContent.ParseRaw(evt.Type)
			prevContent, ok := evt.Unsigned.PrevContent.Parsed.(*event.MemberEventContent)
			if ok && prevContent.Membership != "join" {
				return
			}
		}
		if isSelf {
			portal.handleMatrixLeave(user)
		} else if puppet != nil {
			portal.handleMatrixKick(user, puppet)
		}
	} else if content.Membership == event.MembershipInvite {
		portal.handleMatrixInvite(user, evt)
	}
}

func (mh *matrixHandler) handleReaction(evt *event.Event) {
	if mh.ignoreEvent(evt) {
		return
	}

	portal := mh.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil {
		portal.handleMatrixReaction(evt)
	}
}

func (mh *matrixHandler) handleRedaction(evt *event.Event) {
	if mh.ignoreEvent(evt) {
		return
	}

	portal := mh.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil {
		portal.handleMatrixRedaction(evt)
	}
}
