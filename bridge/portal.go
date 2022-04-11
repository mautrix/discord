package bridge

import (
	"bytes"
	"fmt"
	"strings"
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
	return p.Type == discordgo.ChannelTypeDM
}

func (p *Portal) MainIntent() *appservice.IntentAPI {
	if p.IsPrivateChat() && p.DMUser != "" {
		return p.bridge.GetPuppetByID(p.DMUser).DefaultIntent()
	}

	return p.bridge.bot
}

func (p *Portal) createMatrixRoom(user *User, channel *discordgo.Channel) error {
	// If we have a matrix id the room should exist so we have nothing to do.
	if p.MXID != "" {
		return nil
	}

	p.roomCreateLock.Lock()
	defer p.roomCreateLock.Unlock()

	p.Type = channel.Type
	if p.Type == discordgo.ChannelTypeDM {
		p.DMUser = channel.Recipients[0].ID
	}

	intent := p.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	name, err := p.bridge.Config.Bridge.FormatChannelname(channel, user.Session)
	if err != nil {
		p.log.Warnfln("failed to format name, proceeding with generic name: %v", err)
		p.Name = channel.Name
	} else {
		p.Name = name
	}

	p.Topic = channel.Topic

	// TODO: get avatars figured out
	// p.Avatar = puppet.Avatar
	// p.AvatarURL = puppet.AvatarURL

	p.log.Infoln("Creating Matrix room for channel:", p.Portal.Key.ChannelID)

	initialState := []*event.Event{}

	creationContent := make(map[string]interface{})
	creationContent["m.federate"] = false

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
		p.log.Warnln("Failed to create room:", err)
		return err
	}

	p.MXID = resp.RoomID
	p.Update()
	p.bridge.portalsLock.Lock()
	p.bridge.portalsByMXID[p.MXID] = p
	p.bridge.portalsLock.Unlock()

	p.ensureUserInvited(user)
	user.syncChatDoublePuppetDetails(p, true)

	p.syncParticipants(user, channel.Recipients)

	if p.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByID(p.Key.Receiver)

		chats := map[id.UserID][]id.RoomID{puppet.MXID: {p.MXID}}
		user.updateDirectChats(chats)
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
		p.handleDiscordMessageCreate(msg.user, msg.msg.(*discordgo.MessageCreate).Message)
	case *discordgo.MessageUpdate:
		p.handleDiscordMessagesUpdate(msg.user, msg.msg.(*discordgo.MessageUpdate).Message)
	case *discordgo.MessageDelete:
		p.handleDiscordMessageDelete(msg.user, msg.msg.(*discordgo.MessageDelete).Message)
	case *discordgo.MessageReactionAdd:
		p.handleDiscordReaction(msg.user, msg.msg.(*discordgo.MessageReactionAdd).MessageReaction, true)
	case *discordgo.MessageReactionRemove:
		p.handleDiscordReaction(msg.user, msg.msg.(*discordgo.MessageReactionRemove).MessageReaction, false)
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

func (p *Portal) sendMediaFailedMessage(intent *appservice.IntentAPI, bridgeErr error) {
	content := &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", bridgeErr),
		MsgType: event.MsgNotice,
	}

	_, err := intent.SendMessageEvent(p.MXID, event.EventMessage, content)
	if err != nil {
		p.log.Warnfln("failed to send error message to matrix: %v", err)
	}
}

func (p *Portal) handleDiscordAttachment(intent *appservice.IntentAPI, msgID string, attachment *discordgo.MessageAttachment) {
	// var captionContent *event.MessageEventContent

	// if attachment.Description != "" {
	// 	captionContent = &event.MessageEventContent{
	// 		Body:    attachment.Description,
	// 		MsgType: event.MsgNotice,
	// 	}
	// }
	// p.log.Debugfln("captionContent: %#v", captionContent)

	content := &event.MessageEventContent{
		Body: attachment.Filename,
		Info: &event.FileInfo{
			Height:   attachment.Height,
			MimeType: attachment.ContentType,
			Width:    attachment.Width,

			// This gets overwritten later after the file is uploaded to the homeserver
			Size: attachment.Size,
		},
	}

	switch strings.ToLower(strings.Split(attachment.ContentType, "/")[0]) {
	case "audio":
		content.MsgType = event.MsgAudio
	case "image":
		content.MsgType = event.MsgImage
	case "video":
		content.MsgType = event.MsgVideo
	default:
		content.MsgType = event.MsgFile
	}

	data, err := p.downloadDiscordAttachment(attachment.URL)
	if err != nil {
		p.sendMediaFailedMessage(intent, err)

		return
	}

	err = p.uploadMatrixAttachment(intent, data, content)
	if err != nil {
		p.sendMediaFailedMessage(intent, err)

		return
	}

	resp, err := intent.SendMessageEvent(p.MXID, event.EventMessage, content)
	if err != nil {
		p.log.Warnfln("failed to send media message to matrix: %v", err)
	}

	dbAttachment := p.bridge.db.Attachment.New()
	dbAttachment.Channel = p.Key
	dbAttachment.DiscordMessageID = msgID
	dbAttachment.DiscordAttachmentID = attachment.ID
	dbAttachment.MatrixEventID = resp.EventID
	dbAttachment.Insert()
}

func (p *Portal) handleDiscordMessageCreate(user *User, msg *discordgo.Message) {
	if p.MXID == "" {
		p.log.Warnln("handle message called without a valid portal")

		return
	}

	existing := p.bridge.db.Message.GetByDiscordID(p.Key, msg.ID)
	if existing != nil {
		p.log.Debugln("not handling duplicate message", msg.ID)

		return
	}

	intent := p.bridge.GetPuppetByID(msg.Author.ID).IntentFor(p)

	if msg.Content != "" {
		content := &event.MessageEventContent{
			Body:    msg.Content,
			MsgType: event.MsgText,
		}

		resp, err := intent.SendMessageEvent(p.MXID, event.EventMessage, content)
		if err != nil {
			p.log.Warnfln("failed to send message %q to matrix: %v", msg.ID, err)

			return
		}

		ts, _ := msg.Timestamp.Parse()
		p.markMessageHandled(existing, msg.ID, resp.EventID, msg.Author.ID, ts)
	}

	// now run through any attachments the message has
	for _, attachment := range msg.Attachments {
		p.handleDiscordAttachment(intent, msg.ID, attachment)
	}
}

func (p *Portal) handleDiscordMessagesUpdate(user *User, msg *discordgo.Message) {
	if msg.Author != nil && user.ID == msg.Author.ID {
		return
	}

	if p.MXID == "" {
		p.log.Warnln("handle message called without a valid portal")

		return
	}

	intent := p.bridge.GetPuppetByID(msg.Author.ID).IntentFor(p)

	existing := p.bridge.db.Message.GetByDiscordID(p.Key, msg.ID)
	if existing == nil {
		// Due to the differences in Discord and Matrix attachment handling,
		// existing will return nil if the original message was empty as we
		// don't store/save those messages so we can determine when we're
		// working against an attachment and do the attachment lookup instead.

		// Find all the existing attachments and drop them in a map so we can
		// figure out which, if any have been deleted and clean them up on the
		// matrix side.
		attachmentMap := map[string]*database.Attachment{}
		attachments := p.bridge.db.Attachment.GetAllByDiscordMessageID(p.Key, msg.ID)

		for _, attachment := range attachments {
			attachmentMap[attachment.DiscordAttachmentID] = attachment
		}

		// Now run through the list of attachments on this message and remove
		// them from the map.
		for _, attachment := range msg.Attachments {
			if _, found := attachmentMap[attachment.ID]; found {
				delete(attachmentMap, attachment.ID)
			}
		}

		// Finally run through any attachments still in the map and delete them
		// on the matrix side and our database.
		for _, attachment := range attachmentMap {
			_, err := intent.RedactEvent(p.MXID, attachment.MatrixEventID)
			if err != nil {
				p.log.Warnfln("Failed to remove attachment %s: %v", attachment.MatrixEventID, err)
			}

			attachment.Delete()
		}

		return
	}

	content := &event.MessageEventContent{
		Body:    msg.Content,
		MsgType: event.MsgText,
	}

	content.SetEdit(existing.MatrixID)

	_, err := intent.SendMessageEvent(p.MXID, event.EventMessage, content)
	if err != nil {
		p.log.Warnfln("failed to send message %q to matrix: %v", msg.ID, err)

		return
	}

	// It appears that matrix updates only work against the original event id
	// so updating it to the new one from an edit makes it so you can't update
	// it anyways. So we just don't update anything and we can keep updating
	// the message.
}

func (p *Portal) handleDiscordMessageDelete(user *User, msg *discordgo.Message) {
	// The discord delete message object is pretty empty and doesn't include
	// the author so we have to use the DMUser from the portal that was added
	// at creation time if we're a DM. We'll might have similar issues when we
	// add guild message support, but we'll cross that bridge when we get
	// there.

	// Find the message that we're working with. This could correctly return
	// nil if the message was just one or more attachments.
	existing := p.bridge.db.Message.GetByDiscordID(p.Key, msg.ID)

	var intent *appservice.IntentAPI

	if p.Type == discordgo.ChannelTypeDM {
		intent = p.bridge.GetPuppetByID(p.DMUser).IntentFor(p)
	} else {
		p.log.Errorfln("no guilds yet...")
	}

	if existing != nil {
		_, err := intent.RedactEvent(p.MXID, existing.MatrixID)
		if err != nil {
			p.log.Warnfln("Failed to remove message %s: %v", existing.MatrixID, err)
		}

		existing.Delete()
	}

	// Now delete all of the existing attachments.
	attachments := p.bridge.db.Attachment.GetAllByDiscordMessageID(p.Key, msg.ID)
	for _, attachment := range attachments {
		_, err := intent.RedactEvent(p.MXID, attachment.MatrixEventID)
		if err != nil {
			p.log.Warnfln("Failed to remove attachment %s: %v", attachment.MatrixEventID, err)
		}

		attachment.Delete()
	}
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
	default:
		p.log.Debugln("unknown event type", msg.evt.Type)
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

	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace {
		existing := p.bridge.db.Message.GetByMatrixID(p.Key, content.RelatesTo.EventID)

		if existing != nil && existing.DiscordID != "" {
			// we don't have anything to save for the update message right now
			// as we're not tracking edited timestamps.
			_, err := sender.Session.ChannelMessageEdit(p.Key.ChannelID,
				existing.DiscordID, content.NewContent.Body)
			if err != nil {
				p.log.Errorln("Failed to update message %s: %v", existing.DiscordID, err)

				return
			}
		}

		return
	}

	var msg *discordgo.Message
	var err error

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		msg, err = sender.Session.ChannelMessageSend(p.Key.ChannelID, content.Body)
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := p.downloadMatrixAttachment(evt.ID, content)
		if err != nil {
			p.log.Errorfln("Failed to download matrix attachment: %v", err)

			return
		}

		msgSend := &discordgo.MessageSend{
			Files: []*discordgo.File{
				&discordgo.File{
					Name:        content.Body,
					ContentType: content.Info.MimeType,
					Reader:      bytes.NewReader(data),
				},
			},
		}

		msg, err = sender.Session.ChannelMessageSendComplex(p.Key.ChannelID, msgSend)
	default:
		p.log.Warnln("unknown message type:", content.MsgType)
		return
	}

	if err != nil {
		p.log.Errorfln("Failed to send message: %v", err)

		return
	}

	if msg != nil {
		dbMsg := p.bridge.db.Message.New()
		dbMsg.Channel = p.Key
		dbMsg.DiscordID = msg.ID
		dbMsg.MatrixID = evt.ID
		dbMsg.AuthorID = sender.ID
		dbMsg.Timestamp = time.Now()
		dbMsg.Insert()
	}
}

func (p *Portal) handleMatrixLeave(sender *User) {
	p.log.Debugln("User left private chat portal, cleaning up and deleting...")
	p.delete()
	p.cleanup(false)

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

func (p *Portal) handleMatrixReaction(evt *event.Event) {
	user := p.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		p.log.Errorf("failed to find user for %s", evt.Sender)

		return
	}

	if user.ID != p.Key.Receiver {
		return
	}

	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		p.log.Errorfln("Ignoring reaction %s due to unknown m.relates_to data", evt.ID)

		return
	}

	var discordID string

	msg := p.bridge.db.Message.GetByMatrixID(p.Key, reaction.RelatesTo.EventID)

	// Due to the differences in attachments between Discord and Matrix, if a
	// user reacts to a media message on discord our lookup above will fail
	// because the relation of matrix media messages to attachments in handled
	// in the attachments table instead of messages so we need to check that
	// before continuing.
	//
	// This also leads to interesting problems when a Discord message comes in
	// with multiple attachments. A user can react to each one individually on
	// Matrix, which will cause us to send it twice. Discord tends to ignore
	// this, but if the user removes one of them, discord removes it and now
	// they're out of sync. Perhaps we should add a counter to the reactions
	// table to keep them in sync and to avoid sending duplicates to Discord.
	if msg == nil {
		attachment := p.bridge.db.Attachment.GetByMatrixID(p.Key, reaction.RelatesTo.EventID)
		discordID = attachment.DiscordMessageID
	} else {
		if msg.DiscordID == "" {
			p.log.Debugf("Message %s has not yet been sent to discord", reaction.RelatesTo.EventID)

			return
		}

		discordID = msg.DiscordID
	}

	// Figure out if this is a custom emoji or not.
	emojiID := reaction.RelatesTo.Key
	if strings.HasPrefix(emojiID, "mxc://") {
		uri, _ := id.ParseContentURI(emojiID)
		emoji := p.bridge.db.Emoji.GetByMatrixURL(uri)
		if emoji == nil {
			p.log.Errorfln("failed to find emoji for %s", emojiID)

			return
		}

		emojiID = emoji.APIName()
	}

	err := user.Session.MessageReactionAdd(p.Key.ChannelID, discordID, emojiID)
	if err != nil {
		p.log.Debugf("Failed to send reaction %s id:%s: %v", p.Key, discordID, err)

		return
	}

	dbReaction := p.bridge.db.Reaction.New()
	dbReaction.Channel.ChannelID = p.Key.ChannelID
	dbReaction.Channel.Receiver = p.Key.Receiver
	dbReaction.MatrixEventID = evt.ID
	dbReaction.DiscordMessageID = discordID
	dbReaction.AuthorID = user.ID
	dbReaction.MatrixName = reaction.RelatesTo.Key
	dbReaction.DiscordID = emojiID
	dbReaction.Insert()
}

func (p *Portal) handleDiscordReaction(user *User, reaction *discordgo.MessageReaction, add bool) {
	if user.ID == reaction.UserID {
		return
	}

	intent := p.bridge.GetPuppetByID(reaction.UserID).IntentFor(p)

	var discordID string
	var matrixID string

	if reaction.Emoji.ID != "" {
		dbEmoji := p.bridge.db.Emoji.GetByDiscordID(reaction.Emoji.ID)

		if dbEmoji == nil {
			data, mimeType, err := p.downloadDiscordEmoji(reaction.Emoji.ID, reaction.Emoji.Animated)
			if err != nil {
				p.log.Warnfln("Failed to download emoji %s from discord: %v", reaction.Emoji.ID, err)

				return
			}

			uri, err := p.uploadMatrixEmoji(intent, data, mimeType)
			if err != nil {
				p.log.Warnfln("Failed to upload discord emoji %s to homeserver: %v", reaction.Emoji.ID, err)

				return
			}

			dbEmoji = p.bridge.db.Emoji.New()
			dbEmoji.DiscordID = reaction.Emoji.ID
			dbEmoji.DiscordName = reaction.Emoji.Name
			dbEmoji.MatrixURL = uri
			dbEmoji.Insert()
		}

		discordID = dbEmoji.DiscordID
		matrixID = dbEmoji.MatrixURL.String()
	} else {
		discordID = reaction.Emoji.Name
		matrixID = reaction.Emoji.Name
	}

	// Find the message that we're working with.
	message := p.bridge.db.Message.GetByDiscordID(p.Key, reaction.MessageID)
	if message == nil {
		p.log.Debugfln("failed to add reaction to message %s: message not found", reaction.MessageID)

		return
	}

	// Lookup an existing reaction
	existing := p.bridge.db.Reaction.GetByDiscordID(p.Key, message.DiscordID, discordID)

	if !add {
		if existing == nil {
			p.log.Debugln("Failed to remove reaction for unknown message", reaction.MessageID)

			return
		}

		_, err := intent.RedactEvent(p.MXID, existing.MatrixEventID)
		if err != nil {
			p.log.Warnfln("Failed to remove reaction from %s: %v", p.MXID, err)
		}

		existing.Delete()

		return
	}

	content := event.Content{Parsed: &event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			EventID: message.MatrixID,
			Type:    event.RelAnnotation,
			Key:     matrixID,
		},
	}}

	resp, err := intent.Client.SendMessageEvent(p.MXID, event.EventReaction, &content)
	if err != nil {
		p.log.Errorfln("failed to send reaction from %s: %v", reaction.MessageID, err)

		return
	}

	if existing == nil {
		dbReaction := p.bridge.db.Reaction.New()
		dbReaction.Channel = p.Key
		dbReaction.DiscordMessageID = message.DiscordID
		dbReaction.MatrixEventID = resp.EventID
		dbReaction.AuthorID = reaction.UserID

		dbReaction.MatrixName = matrixID
		dbReaction.DiscordID = discordID

		dbReaction.Insert()
	}
}

func (p *Portal) handleMatrixRedaction(evt *event.Event) {
	user := p.bridge.GetUserByMXID(evt.Sender)

	if user.ID != p.Key.Receiver {
		return
	}

	// First look if we're redacting a message
	message := p.bridge.db.Message.GetByMatrixID(p.Key, evt.Redacts)
	if message != nil {
		if message.DiscordID != "" {
			err := user.Session.ChannelMessageDelete(p.Key.ChannelID, message.DiscordID)
			if err != nil {
				p.log.Debugfln("Failed to delete discord message %s: %v", message.DiscordID, err)
			} else {
				message.Delete()
			}
		}

		return
	}

	// Now check if it's a reaction.
	reaction := p.bridge.db.Reaction.GetByMatrixID(p.Key, evt.Redacts)
	if reaction != nil {
		if reaction.DiscordID != "" {
			err := user.Session.MessageReactionRemove(p.Key.ChannelID, reaction.DiscordMessageID, reaction.DiscordID, reaction.AuthorID)
			if err != nil {
				p.log.Debugfln("Failed to delete reaction %s for message %s: %v", reaction.DiscordID, reaction.DiscordMessageID, err)
			} else {
				reaction.Delete()
			}
		}

		return
	}

	p.log.Warnfln("Failed to redact %s@%s: no event found", p.Key, evt.Redacts)
}

func (p *Portal) update(user *User, channel *discordgo.Channel) {
	name, err := p.bridge.Config.Bridge.FormatChannelname(channel, user.Session)
	if err != nil {
		p.log.Warnln("Failed to format channel name, using existing:", err)
	} else {
		p.Name = name
	}

	intent := p.MainIntent()

	if p.Name != name {
		_, err = intent.SetRoomName(p.MXID, p.Name)
		if err != nil {
			p.log.Warnln("Failed to update room name:", err)
		}
	}

	if p.Topic != channel.Topic {
		p.Topic = channel.Topic
		_, err = intent.SetRoomTopic(p.MXID, p.Topic)
		if err != nil {
			p.log.Warnln("Failed to update room topic:", err)
		}
	}

	if p.Avatar != channel.Icon {
		p.Avatar = channel.Icon

		var url string

		if p.Type == discordgo.ChannelTypeDM {
			dmUser, err := user.Session.User(p.DMUser)
			if err != nil {
				p.log.Warnln("failed to lookup the dmuser", err)
			} else {
				url = dmUser.AvatarURL("")
			}
		} else {
			url = discordgo.EndpointGroupIcon(channel.ID, channel.Icon)
		}

		p.AvatarURL = id.ContentURI{}
		if url != "" {
			uri, err := uploadAvatar(intent, url)
			if err != nil {
				p.log.Warnf("failed to upload avatar", err)
			} else {
				p.AvatarURL = uri
			}
		}

		intent.SetRoomAvatar(p.MXID, p.AvatarURL)
	}

	p.Update()
	p.log.Debugln("portal updated")
}
