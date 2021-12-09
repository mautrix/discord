package bridge

import (
	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type matrixHandler struct {
	as     *appservice.AppService
	bridge *Bridge
	log    maulogger.Logger
}

func (b *Bridge) setupEvents() {
	b.eventProcessor = appservice.NewEventProcessor(b.as)

	b.matrixHandler = &matrixHandler{
		as:     b.as,
		bridge: b,
		log:    b.log.Sub("Matrix"),
	}

	b.eventProcessor.On(event.EventMessage, b.matrixHandler.handleMessage)
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

	mh.log.Debugfln("received message from %q: %q", evt.Sender, evt.Content.AsMessage())
}

func (mh *matrixHandler) handleMembership(evt *event.Event) {
	mh.log.Debugfln("recevied invite %#v\n", evt)

	// Return early if we're supposed to ignore the event.
	if mh.ignoreEvent(evt) {
		return
	}

	// Grab the content of the event.
	content := evt.Content.AsMember()

	// TODO: handle invites from ourselfs?

	isSelf := id.UserID(evt.GetStateKey()) == evt.Sender

	// Handle matrix invites.
	if content.Membership == event.MembershipInvite && !isSelf {
		//
	}
}
