package bridge

import (
	"fmt"
	"sync"

	"github.com/bwmarrin/discordgo"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/database"
)

type portalDiscordMessage struct {
	msg  interface{}
	user *User
}

type portalMatrixMessage struct {
	evt  *event.Event
	user *User
}

type Portal struct {
	*database.Portal

	bridge *Bridge
	log    log.Logger

	channelType discordgo.ChannelType

	roomCreateLock sync.Mutex

	discordMessages chan portalDiscordMessage
	matrixMessages  chan portalMatrixMessage
}

var (
	portalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
)

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

func (b *Bridge) GetPortalByID(key database.PortalKey) *Portal {
	b.portalsLock.Lock()
	defer b.portalsLock.Unlock()

	portal, ok := b.portalsByID[key]
	if !ok {
		return b.loadPortal(b.db.Portal.GetByID(key), &key)
	}

	return portal
}

func (b *Bridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: b,
		log:    b.log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		discordMessages: make(chan portalDiscordMessage, b.Config.Bridge.PortalMessageBuffer),
		matrixMessages:  make(chan portalMatrixMessage, b.Config.Bridge.PortalMessageBuffer),
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
			p.log.Infoln("got matrix message", msg)
		case msg := <-p.discordMessages:
			p.handleDiscordMessage(msg)
		}
	}
}

func (p *Portal) IsPrivateChat() bool {
	return (p.channelType == discordgo.ChannelTypeDM || p.channelType == discordgo.ChannelTypeGroupDM)
}

func (p *Portal) MainIntent() *appservice.IntentAPI {
	if p.IsPrivateChat() {
		return p.bridge.GetPuppetByID(p.Key.ID).DefaultIntent()
	}

	return p.bridge.bot
}

func (p *Portal) createMatrixRoom(user *User, channel *discordgo.Channel) error {
	p.roomCreateLock.Lock()
	defer p.roomCreateLock.Unlock()

	// If we have a matrix id the room should exist so we have nothing to do.
	if p.MXID != "" {
		return nil
	}

	p.channelType = channel.Type

	intent := p.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	if p.IsPrivateChat() {
		puppet := p.bridge.GetPuppetByID(p.Key.ID)
		puppet.SyncContact(user)

		p.Name = puppet.DisplayName
		p.Avatar = puppet.Avatar
		p.AvatarURL = puppet.AvatarURL
	}

	p.log.Infoln("Creating Matrix room. Info source:", p.Portal.Key.ID)

	initialState := []*event.Event{}

	creationContent := make(map[string]interface{})
	// if !portal.bridge.Config.Bridge.FederateRooms {
	creationContent["m.federate"] = false
	// }

	var invite []id.UserID

	if p.IsPrivateChat() {
		invite = append(invite, p.bridge.bot.UserID)
	}

	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            p.Name,
		Topic:           p.Topic,
		Preset:          "private_chat",
		IsDirect:        p.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		return err
	}

	p.MXID = resp.RoomID
	p.Update()
	p.bridge.portalsLock.Lock()
	p.bridge.portalsByMXID[p.MXID] = p
	p.bridge.portalsLock.Unlock()

	p.ensureUserInvited(user)

	// if p.IsPrivateChat() {
	// 	puppet := user.bridge.GetPuppetByID(p.Key.ID)

	// if p.bridge.Config.Bridge.Encryption.Default {
	// 	err = portal.bridge.Bot.EnsureJoined(portal.MXID)
	// 	if err != nil {
	// 		portal.log.Errorln("Failed to join created portal with bridge bot for e2be:", err)
	// 	}
	// }

	// user.UpdateDirectChats(map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}})
	// }

	firstEventResp, err := p.MainIntent().SendMessageEvent(p.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		p.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		p.FirstEventID = firstEventResp.EventID
		p.Update()
	}

	return nil
}

func (p *Portal) handleDiscordMessage(msg portalDiscordMessage) {
	if p.MXID == "" {
		p.log.Debugln("Creating Matrix room from incoming message")

		discordMsg := msg.msg.(*discordgo.MessageCreate)
		channel, err := msg.user.Session.Channel(discordMsg.ChannelID)
		if err != nil {
			p.log.Errorln("Failed to find channel for message:", err)

			return
		}

		if err := p.createMatrixRoom(msg.user, channel); err != nil {
			p.log.Errorln("Failed to create portal room:", err)

			return
		}
	}

	switch msg.msg.(type) {
	case *discordgo.MessageCreate:
		p.handleMessage(msg.msg.(*discordgo.MessageCreate).Message)
	default:
		p.log.Warnln("unknown message type")
	}
}

func (p *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(p.MainIntent(), p.MXID, p.IsPrivateChat())
}

func (p *Portal) handleMessage(msg *discordgo.Message) {
	if p.MXID == "" {
		p.log.Warnln("handle message called without a valid portal")

		return
	}

	// TODO: Check if we already got the message

	p.log.Debugln("content", msg.Content)
	p.log.Debugln("embeds", msg.Embeds)
	p.log.Debugln("msg", msg)

	content := &event.MessageEventContent{
		Body:    msg.Content,
		MsgType: event.MsgText,
	}

	resp, err := p.MainIntent().SendMessageEvent(p.MXID, event.EventMessage, content)
	p.log.Warnln("response:", resp)
	p.log.Warnln("error:", err)
}
