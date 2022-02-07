package bridge

import (
	"fmt"
	"sync"
	"time"

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

	channel *discordgo.Channel

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

func (b *Bridge) GetAllPortals() []*Portal {
	return b.dbPortalsToPortals(b.db.Portal.GetAll())
}

func (b *Bridge) GetAllPortalsByID(id string) []*Portal {
	return b.dbPortalsToPortals(b.db.Portal.GetAllByID(id))
}

func (b *Bridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	b.portalsLock.Lock()
	defer b.portalsLock.Unlock()

	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}

		portal, ok := b.portalsByID[dbPortal.Key]
		if !ok {
			portal = b.loadPortal(dbPortal, nil)
		}

		output[index] = portal
	}

	return output
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

func (p *Portal) handleMatrixInvite(sender *User, evt *event.Event) {
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
			p.handleMatrixMessages(msg)
		case msg := <-p.discordMessages:
			p.handleDiscordMessages(msg)
		}
	}
}

func (p *Portal) IsPrivateChat() bool {
	if p.channel != nil {
		return p.channel.Type == discordgo.ChannelTypeDM
	}

	return false
}

func (p *Portal) MainIntent() *appservice.IntentAPI {
	if p.IsPrivateChat() && p.channel != nil && len(p.channel.Recipients) == 1 {
		return p.bridge.GetPuppetByID(p.channel.Recipients[0].ID).DefaultIntent()
	}

	return p.bridge.bot
}

func (p *Portal) getMessagePuppet(user *User, message *discordgo.Message) *Puppet {
	p.log.Debugf("getMessagePuppet")
	if message.Author.ID == user.ID {
		return p.bridge.GetPuppetByID(user.ID)
	}

	puppet := p.bridge.GetPuppetByID(message.Author.ID)
	puppet.SyncContact(user)

	return puppet
}

func (p *Portal) getMessageIntent(user *User, message *discordgo.Message) *appservice.IntentAPI {
	return p.getMessagePuppet(user, message).IntentFor(p)
}

func (p *Portal) createMatrixRoom(user *User, channel *discordgo.Channel) error {
	p.channel = channel

	p.roomCreateLock.Lock()
	defer p.roomCreateLock.Unlock()

	// If we have a matrix id the room should exist so we have nothing to do.
	if p.MXID != "" {
		return nil
	}

	intent := p.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	// if p.IsPrivateChat() {
	p.Name = channel.Name
	p.Topic = channel.Topic

	// TODO: get avatars figured out
	// p.Avatar = puppet.Avatar
	// p.AvatarURL = puppet.AvatarURL
	// }

	p.log.Infoln("Creating Matrix room for channel:", p.Portal.Key.ChannelID)

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

	p.log.Debugln("inviting user", user)
	p.ensureUserInvited(user)

	if p.IsPrivateChat() {
		p.syncParticipants(user, channel.Recipients)
	}

	firstEventResp, err := p.MainIntent().SendMessageEvent(p.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		p.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		p.FirstEventID = firstEventResp.EventID
		p.Update()
	}

	return nil
}

func (p *Portal) handleDiscordMessages(msg portalDiscordMessage) {
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
		p.handleDiscordMessage(msg.msg.(*discordgo.MessageCreate).Message)
	default:
		p.log.Warnln("unknown message type")
	}
}

func (p *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(p.MainIntent(), p.MXID, p.IsPrivateChat())
}

func (p *Portal) markMessageHandled(msg *database.Message, discordID string, mxid id.EventID, authorID string, timestamp time.Time) *database.Message {
	if msg == nil {
		msg := p.bridge.db.Message.New()
		msg.Channel = p.Key
		msg.DiscordID = discordID
		msg.MatrixID = mxid
		msg.AuthorID = authorID
		msg.Timestamp = timestamp
		msg.Insert()
	} else {
		msg.UpdateMatrixID(mxid)
	}

	return msg
}

func (p *Portal) handleDiscordMessage(msg *discordgo.Message) {
	if p.MXID == "" {
		p.log.Warnln("handle message called without a valid portal")

		return
	}

	existing := p.bridge.db.Message.GetByDiscordID(p.Key, msg.ID)
	if existing != nil {
		p.log.Debugln("not handling duplicate message", msg.ID)

		return
	}

	content := &event.MessageEventContent{
		Body:    msg.Content,
		MsgType: event.MsgText,
	}

	intent := p.bridge.GetPuppetByID(msg.Author.ID).IntentFor(p)

	resp, err := intent.SendMessageEvent(p.MXID, event.EventMessage, content)
	if err != nil {
		p.log.Warnfln("failed to send message %q to matrix: %v", msg.ID, err)
		return
	}

	ts, _ := msg.Timestamp.Parse()
	p.markMessageHandled(nil, msg.ID, resp.EventID, msg.Author.ID, ts)
}

func (p *Portal) syncParticipants(source *User, participants []*discordgo.User) {
	for _, participant := range participants {
		puppet := p.bridge.GetPuppetByID(participant.ID)
		puppet.SyncContact(source)

		user := p.bridge.GetUserByID(participant.ID)
		if user != nil {
			p.ensureUserInvited(user)
		}

		if user == nil || !puppet.IntentFor(p).IsCustomPuppet {
			if err := puppet.IntentFor(p).EnsureJoined(p.MXID); err != nil {
				p.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.ID, p.MXID, err)
			}
		}
	}
}

func (p *Portal) handleMatrixMessages(msg portalMatrixMessage) {
	switch msg.evt.Type {
	case event.EventMessage:
		p.handleMatrixMessage(msg.user, msg.evt)
	}
}

func (p *Portal) handleMatrixMessage(sender *User, evt *event.Event) {
	if p.IsPrivateChat() && sender.ID != p.Key.Receiver {
		return
	}

	existing := p.bridge.db.Message.GetByMatrixID(p.Key, evt.ID)
	if existing != nil {
		p.log.Debugln("not handling duplicate message", evt.ID)

		return
	}

	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		p.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)

		return
	}

	msg, err := sender.Session.ChannelMessageSend(p.Key.ChannelID, content.Body)
	if err != nil {
		p.log.Errorfln("Failed to send message: %v", err)

		return
	}

	dbMsg := p.bridge.db.Message.New()
	dbMsg.Channel = p.Key
	dbMsg.DiscordID = msg.ID
	dbMsg.MatrixID = evt.ID
	dbMsg.AuthorID = sender.ID
	dbMsg.Timestamp = time.Now()
	dbMsg.Insert()
}

func (p *Portal) handleMatrixLeave(sender *User) {
	if p.IsPrivateChat() {
		p.log.Debugln("User left private chat portal, cleaning up and deleting...")
		p.delete()
		p.cleanup(false)

		return
	}

	// TODO: figure out how to close a dm from the API.

	p.cleanupIfEmpty()
}

func (p *Portal) delete() {
	p.Portal.Delete()
	p.bridge.portalsLock.Lock()
	delete(p.bridge.portalsByID, p.Key)

	if p.MXID != "" {
		delete(p.bridge.portalsByMXID, p.MXID)
	}

	p.bridge.portalsLock.Unlock()
}

func (p *Portal) cleanupIfEmpty() {
	users, err := p.getMatrixUsers()
	if err != nil {
		p.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)

		return
	}

	if len(users) == 0 {
		p.log.Infoln("Room seems to be empty, cleaning up...")
		p.delete()
		p.cleanup(false)
	}
}

func (p *Portal) cleanup(puppetsOnly bool) {
	if p.MXID != "" {
		return
	}

	if p.IsPrivateChat() {
		_, err := p.MainIntent().LeaveRoom(p.MXID)
		if err != nil {
			p.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}

		return
	}

	intent := p.MainIntent()
	members, err := intent.JoinedMembers(p.MXID)
	if err != nil {
		p.log.Errorln("Failed to get portal members for cleanup:", err)

		return
	}

	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}

		puppet := p.bridge.GetPuppetByMXID(member)
		if p != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(p.MXID)
			if err != nil {
				p.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(p.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				p.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}

	_, err = intent.LeaveRoom(p.MXID)
	if err != nil {
		p.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (p *Portal) getMatrixUsers() ([]id.UserID, error) {
	members, err := p.MainIntent().JoinedMembers(p.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}

	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := p.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != p.bridge.bot.UserID {
			users = append(users, userID)
		}
	}

	return users, nil
}

func (p *Portal) handleMatrixKick(sender *User, target *Puppet) {
	// TODO: need to learn how to make this happen as discordgo proper doesn't
	// support group dms and it looks like it's a binary blob.
}
