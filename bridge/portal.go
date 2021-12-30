package bridge

import (
	"fmt"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/database"
)

type PortalMatrixMessage struct {
	evt  *event.Event
	user *User
}

type Portal struct {
	*database.Portal

	bridge *Bridge
	log    log.Logger

	matrixMessages chan PortalMatrixMessage
}

func (b *Bridge) loadPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	// If we weren't given a portal we'll attempt to create it if a key was
	// provided.
	if dbPortal == nil {
		if key == nil {
			return nil
		}

		dbPortal = b.db.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}

	portal := b.NewPortal(dbPortal)

	// No need to lock, it is assumed that our callers have already acquired
	// the lock.
	b.portalsByID[portal.Key] = portal
	if portal.MXID != "" {
		b.portalsByMXID[portal.MXID] = portal
	}

	return portal
}

func (b *Bridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	b.portalsLock.Lock()
	defer b.portalsLock.Unlock()

	portal, ok := b.portalsByMXID[mxid]
	if !ok {
		return b.loadPortal(b.db.Portal.GetByMXID(mxid), nil)
	}

	return portal
}

func (b *Bridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: b,
		log:    b.log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		matrixMessages: make(chan PortalMatrixMessage, b.config.Bridge.PortalMessageBuffer),
	}

	go portal.messageLoop()

	return portal
}

func (p *Portal) HandleMatrixInvite(sender *User, evt *event.Event) {
	// Look up an existing puppet or create a new one.
	puppet := p.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	if puppet != nil {
		p.log.Infoln("no puppet for %v", sender)
		// Open a conversation on the discord side?
	}
	p.log.Infoln("puppet:", puppet)
}

func (p *Portal) messageLoop() {
	for {
		select {
		case msg := <-p.matrixMessages:
			p.log.Infoln("got message", msg)
		}
	}
}

func (p *Portal) IsPrivateChat() bool {
	return false
}

func (p *Portal) MainIntent() *appservice.IntentAPI {
	if p.IsPrivateChat() {
		return p.bridge.GetPuppetByID(p.Key.ID).DefaultIntent()
	}

	return p.bridge.bot
}
