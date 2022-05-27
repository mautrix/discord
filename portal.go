package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/util/variationselector"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
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

	bridge *DiscordBridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex

	discordMessages chan portalDiscordMessage
	matrixMessages  chan portalMatrixMessage
}

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update()
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser /*|| portal.HasRelaybot()*/ {
		portal.matrixMessages <- portalMatrixMessage{user: user.(*User), evt: evt}
	}
}

var _ bridge.Portal = (*Portal)(nil)

var (
	portalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
)

func (br *DiscordBridge) loadPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	// If we weren't given a portal we'll attempt to create it if a key was
	// provided.
	if dbPortal == nil {
		if key == nil {
			return nil
		}

		dbPortal = br.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}

	portal := br.NewPortal(dbPortal)

	// No need to lock, it is assumed that our callers have already acquired
	// the lock.
	br.portalsByID[portal.Key] = portal
	if portal.MXID != "" {
		br.portalsByMXID[portal.MXID] = portal
	}

	return portal
}

func (br *DiscordBridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByMXID(mxid), nil)
	}

	return portal
}

func (br *DiscordBridge) GetPortalByID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByID[key]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByID(key), &key)
	}

	return portal
}

func (br *DiscordBridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll())
}

func (br *DiscordBridge) GetDMPortalsWith(otherUserID string) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.FindPrivateChatsWith(otherUserID))
}

func (br *DiscordBridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}

		portal, ok := br.portalsByID[dbPortal.Key]
		if !ok {
			portal = br.loadPortal(dbPortal, nil)
		}

		output[index] = portal
	}

	return output
}

func (br *DiscordBridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: br,
		log:    br.Log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		discordMessages: make(chan portalDiscordMessage, br.Config.Bridge.PortalMessageBuffer),
		matrixMessages:  make(chan portalMatrixMessage, br.Config.Bridge.PortalMessageBuffer),
	}

	go portal.messageLoop()

	return portal
}

func (portal *Portal) messageLoop() {
	for {
		select {
		case msg := <-portal.matrixMessages:
			portal.handleMatrixMessages(msg)
		case msg := <-portal.discordMessages:
			portal.handleDiscordMessages(msg)
		}
	}
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Type == discordgo.ChannelTypeDM
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() && portal.OtherUserID != "" {
		return portal.bridge.GetPuppetByID(portal.OtherUserID).DefaultIntent()
	}

	return portal.bridge.Bot
}

func (portal *Portal) createMatrixRoom(user *User, channel *discordgo.Channel) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if portal.MXID != "" {
		return nil
	}

	portal.Type = channel.Type
	if portal.Type == discordgo.ChannelTypeDM {
		portal.OtherUserID = channel.Recipients[0].ID
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	name, err := portal.bridge.Config.Bridge.FormatChannelname(channel, user.Session)
	if err != nil {
		portal.log.Warnfln("failed to format name, proceeding with generic name: %v", err)
		portal.Name = channel.Name
	} else {
		portal.Name = name
	}

	portal.Topic = channel.Topic

	// TODO: get avatars figured out
	// portal.Avatar = puppet.Avatar
	// portal.AvatarURL = puppet.AvatarURL

	portal.log.Infoln("Creating Matrix room for channel:", portal.Portal.Key.ChannelID)

	initialState := []*event.Event{}

	creationContent := make(map[string]interface{})
	if !portal.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}

	var invite []id.UserID

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1},
			},
		})
		portal.Encrypted = true

		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
		}
	}

	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            portal.Name,
		Topic:           portal.Topic,
		Invite:          invite,
		Preset:          "private_chat",
		IsDirect:        portal.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		portal.log.Warnln("Failed to create room:", err)
		return err
	}

	portal.MXID = resp.RoomID
	portal.Update()
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()

	portal.ensureUserInvited(user)
	user.syncChatDoublePuppetDetails(portal, true)

	portal.syncParticipants(user, channel.Recipients)

	if portal.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByID(portal.Key.Receiver)

		chats := map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}}
		user.updateDirectChats(chats)
	}

	firstEventResp, err := portal.MainIntent().SendMessageEvent(portal.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		portal.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		portal.FirstEventID = firstEventResp.EventID
		portal.Update()
	}

	return nil
}

func (portal *Portal) handleDiscordMessages(msg portalDiscordMessage) {
	if portal.MXID == "" {
		discordMsg, ok := msg.msg.(*discordgo.MessageCreate)
		if !ok {
			portal.log.Warnln("Can't create Matrix room from non new message event")
			return
		}

		portal.log.Debugln("Creating Matrix room from incoming message")

		channel, err := msg.user.Session.Channel(discordMsg.ChannelID)
		if err != nil {
			portal.log.Errorln("Failed to find channel for message:", err)

			return
		}

		if err := portal.createMatrixRoom(msg.user, channel); err != nil {
			portal.log.Errorln("Failed to create portal room:", err)

			return
		}
	}

	switch msg.msg.(type) {
	case *discordgo.MessageCreate:
		portal.handleDiscordMessageCreate(msg.user, msg.msg.(*discordgo.MessageCreate).Message)
	case *discordgo.MessageUpdate:
		portal.handleDiscordMessagesUpdate(msg.user, msg.msg.(*discordgo.MessageUpdate).Message)
	case *discordgo.MessageDelete:
		portal.handleDiscordMessageDelete(msg.user, msg.msg.(*discordgo.MessageDelete).Message)
	case *discordgo.MessageReactionAdd:
		portal.handleDiscordReaction(msg.user, msg.msg.(*discordgo.MessageReactionAdd).MessageReaction, true)
	case *discordgo.MessageReactionRemove:
		portal.handleDiscordReaction(msg.user, msg.msg.(*discordgo.MessageReactionRemove).MessageReaction, false)
	default:
		portal.log.Warnln("unknown message type")
	}
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) markMessageHandled(discordID string, mxid id.EventID, authorID string, timestamp time.Time) *database.Message {
	msg := portal.bridge.DB.Message.New()
	msg.Channel = portal.Key
	msg.DiscordID = discordID
	msg.MXID = mxid
	msg.SenderID = authorID
	msg.Timestamp = timestamp
	msg.Insert()
	return msg
}

func (portal *Portal) sendMediaFailedMessage(intent *appservice.IntentAPI, bridgeErr error) {
	content := &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", bridgeErr),
		MsgType: event.MsgNotice,
	}

	_, err := portal.sendMatrixMessage(intent, event.EventMessage, content, nil, time.Now().UTC().UnixMilli())
	if err != nil {
		portal.log.Warnfln("failed to send error message to matrix: %v", err)
	}
}

func (portal *Portal) handleDiscordAttachment(intent *appservice.IntentAPI, msgID string, attachment *discordgo.MessageAttachment) {
	// var captionContent *event.MessageEventContent

	// if attachment.Description != "" {
	// 	captionContent = &event.MessageEventContent{
	// 		Body:    attachment.Description,
	// 		MsgType: event.MsgNotice,
	// 	}
	// }
	// portal.Log.Debugfln("captionContent: %#v", captionContent)

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

	data, err := portal.downloadDiscordAttachment(attachment.URL)
	if err != nil {
		portal.sendMediaFailedMessage(intent, err)

		return
	}

	err = portal.uploadMatrixAttachment(intent, data, content)
	if err != nil {
		portal.sendMediaFailedMessage(intent, err)

		return
	}

	resp, err := portal.sendMatrixMessage(intent, event.EventMessage, content, nil, time.Now().UTC().UnixMilli())
	if err != nil {
		portal.log.Warnfln("failed to send media message to matrix: %v", err)
	}

	dbAttachment := portal.bridge.DB.Attachment.New()
	dbAttachment.Channel = portal.Key
	dbAttachment.DiscordMessageID = msgID
	dbAttachment.DiscordAttachmentID = attachment.ID
	dbAttachment.MXID = resp.EventID
	dbAttachment.Insert()
}

func (portal *Portal) handleDiscordMessageCreate(user *User, msg *discordgo.Message) {
	if portal.MXID == "" {
		portal.log.Warnln("handle message called without a valid portal")

		return
	}

	// Handle room name changes
	if msg.Type == discordgo.MessageTypeChannelNameChange {
		channel, err := user.Session.Channel(msg.ChannelID)
		if err != nil {
			portal.log.Errorf("Failed to find the channel for portal %s", portal.Key)
			return
		}

		name, err := portal.bridge.Config.Bridge.FormatChannelname(channel, user.Session)
		if err != nil {
			portal.log.Errorf("Failed to format name for portal %s", portal.Key)
			return
		}

		portal.Name = name
		portal.Update()

		portal.MainIntent().SetRoomName(portal.MXID, name)

		return
	}

	// Handle normal message
	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing != nil {
		portal.log.Debugln("not handling duplicate message", msg.ID)

		return
	}

	puppet := portal.bridge.GetPuppetByID(msg.Author.ID)
	puppet.SyncContact(user)
	intent := puppet.IntentFor(portal)

	if msg.Content != "" {
		content := &event.MessageEventContent{
			Body:    msg.Content,
			MsgType: event.MsgText,
		}

		if msg.MessageReference != nil && msg.MessageReference.ChannelID == portal.Key.ChannelID {
			//key := database.PortalKey{msg.MessageReference.ChannelID, user.ID}
			replyTo := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.MessageReference.MessageID)

			if replyTo != nil {
				content.RelatesTo = &event.RelatesTo{
					Type:    event.RelReply,
					EventID: existing.MXID,
				}
			}
		}

		resp, err := portal.sendMatrixMessage(intent, event.EventMessage, content, nil, time.Now().UTC().UnixMilli())
		if err != nil {
			portal.log.Warnfln("failed to send message %q to matrix: %v", msg.ID, err)

			return
		}

		ts, _ := msg.Timestamp.Parse()
		portal.markMessageHandled(msg.ID, resp.EventID, msg.Author.ID, ts)
	}

	// now run through any attachments the message has
	for _, attachment := range msg.Attachments {
		portal.handleDiscordAttachment(intent, msg.ID, attachment)
	}
}

func (portal *Portal) handleDiscordMessagesUpdate(user *User, msg *discordgo.Message) {
	if portal.MXID == "" {
		portal.log.Warnln("handle message called without a valid portal")

		return
	}

	// There's a few scenarios where the author is nil but I haven't figured
	// them all out yet.
	if msg.Author == nil {
		// If the server has to lookup opengraph previews it'll send the
		// message through without the preview and then add the preview later
		// via a message update. However, when it does this there is no author
		// as it's just the server, so for the moment we'll ignore this to
		// avoid a crash.
		if len(msg.Embeds) > 0 {
			portal.log.Debugln("ignoring update for opengraph attachment")

			return
		}

		portal.log.Errorfln("author is nil: %#v", msg)
	}

	intent := portal.bridge.GetPuppetByID(msg.Author.ID).IntentFor(portal)

	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing == nil {
		// Due to the differences in Discord and Matrix attachment handling,
		// existing will return nil if the original message was empty as we
		// don't store/save those messages so we can determine when we're
		// working against an attachment and do the attachment lookup instead.

		// Find all the existing attachments and drop them in a map so we can
		// figure out which, if any have been deleted and clean them up on the
		// matrix side.
		attachmentMap := map[string]*database.Attachment{}
		attachments := portal.bridge.DB.Attachment.GetAllByDiscordMessageID(portal.Key, msg.ID)

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
			_, err := intent.RedactEvent(portal.MXID, attachment.MXID)
			if err != nil {
				portal.log.Warnfln("Failed to remove attachment %s: %v", attachment.MXID, err)
			}

			attachment.Delete()
		}

		return
	}

	content := &event.MessageEventContent{
		Body:    msg.Content,
		MsgType: event.MsgText,
	}

	content.SetEdit(existing.MXID)

	_, err := portal.sendMatrixMessage(intent, event.EventMessage, content, nil, time.Now().UTC().UnixMilli())
	if err != nil {
		portal.log.Warnfln("failed to send message %q to matrix: %v", msg.ID, err)

		return
	}

	//ts, _ := msg.Timestamp.Parse()
	//portal.markMessageHandled(existing, msg.ID, resp.EventID, msg.Author.ID, ts)
}

func (portal *Portal) handleDiscordMessageDelete(user *User, msg *discordgo.Message) {
	// The discord delete message object is pretty empty and doesn't include
	// the author so we have to use the DMUser from the portal that was added
	// at creation time if we're a DM. We'll might have similar issues when we
	// add guild message support, but we'll cross that bridge when we get
	// there.

	// Find the message that we're working with. This could correctly return
	// nil if the message was just one or more attachments.
	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	intent := portal.MainIntent()

	if existing != nil {
		_, err := intent.RedactEvent(portal.MXID, existing.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to remove message %s: %v", existing.MXID, err)
		}

		existing.Delete()
	}

	// Now delete all of the existing attachments.
	attachments := portal.bridge.DB.Attachment.GetAllByDiscordMessageID(portal.Key, msg.ID)
	for _, attachment := range attachments {
		_, err := intent.RedactEvent(portal.MXID, attachment.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to remove attachment %s: %v", attachment.MXID, err)
		}

		attachment.Delete()
	}
}

func (portal *Portal) syncParticipants(source *User, participants []*discordgo.User) {
	for _, participant := range participants {
		puppet := portal.bridge.GetPuppetByID(participant.ID)
		puppet.SyncContact(source)

		user := portal.bridge.GetUserByID(participant.ID)
		if user != nil {
			portal.ensureUserInvited(user)
		}

		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			if err := puppet.IntentFor(portal).EnsureJoined(portal.MXID); err != nil {
				portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.ID, portal.MXID, err)
			}
		}
	}
}

func (portal *Portal) encrypt(content *event.Content, eventType event.Type) (event.Type, error) {
	if portal.Encrypted && portal.bridge.Crypto != nil {
		// TODO maybe the locking should be inside mautrix-go?
		portal.encryptLock.Lock()
		encrypted, err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, *content)
		portal.encryptLock.Unlock()
		if err != nil {
			return eventType, fmt.Errorf("failed to encrypt event: %w", err)
		}
		eventType = event.EventEncrypted
		content.Parsed = encrypted
	}
	return eventType, nil
}

const doublePuppetValue = "mautrix-discord"

func (portal *Portal) sendMatrixMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	if timestamp != 0 && intent.IsCustomPuppet {
		if wrappedContent.Raw == nil {
			wrappedContent.Raw = map[string]interface{}{}
		}
		if intent.IsCustomPuppet {
			wrappedContent.Raw[bridge.DoublePuppetKey] = doublePuppetValue
		}
	}
	var err error
	eventType, err = portal.encrypt(&wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	if eventType == event.EventEncrypted {
		// Clear other custom keys if the event was encrypted, but keep the double puppet identifier
		if intent.IsCustomPuppet {
			wrappedContent.Raw = map[string]interface{}{bridge.DoublePuppetKey: doublePuppetValue}
		} else {
			wrappedContent.Raw = nil
		}
	}

	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if timestamp == 0 {
		return intent.SendMessageEvent(portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(portal.MXID, eventType, &wrappedContent, timestamp)
	}
}

func (portal *Portal) handleMatrixMessages(msg portalMatrixMessage) {
	switch msg.evt.Type {
	case event.EventMessage:
		portal.handleMatrixMessage(msg.user, msg.evt)
	case event.EventRedaction:
		portal.handleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(msg.user, msg.evt)
	default:
		portal.log.Debugln("unknown event type", msg.evt.Type)
	}
}

func (portal *Portal) handleMatrixMessage(sender *User, evt *event.Event) {
	if portal.IsPrivateChat() && sender.ID != portal.Key.Receiver {
		return
	}

	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)

		return
	}

	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace {
		edits := portal.bridge.DB.Message.GetByMXID(portal.Key, content.RelatesTo.EventID)

		if edits != nil {
			// we don't have anything to save for the update message right now
			// as we're not tracking edited timestamps.
			_, err := sender.Session.ChannelMessageEdit(portal.Key.ChannelID,
				edits.DiscordID, content.NewContent.Body)
			if err != nil {
				portal.log.Errorln("Failed to update message %s: %v", edits.DiscordID, err)

				return
			}
		}

		return
	}

	var msg *discordgo.Message
	var err error

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		sent := false

		if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReply {
			replyTo := portal.bridge.DB.Message.GetByMXID(
				portal.Key,
				content.RelatesTo.EventID,
			)

			if replyTo != nil {
				msg, err = sender.Session.ChannelMessageSendReply(
					portal.Key.ChannelID,
					content.Body,
					&discordgo.MessageReference{
						ChannelID: portal.Key.ChannelID,
						MessageID: replyTo.DiscordID,
					},
				)
				if err == nil {
					sent = true
				}
			}
		}
		if !sent {
			msg, err = sender.Session.ChannelMessageSend(portal.Key.ChannelID, content.Body)
		}
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := portal.downloadMatrixAttachment(evt.ID, content)
		if err != nil {
			portal.log.Errorfln("Failed to download matrix attachment: %v", err)

			return
		}

		msgSend := &discordgo.MessageSend{
			Files: []*discordgo.File{{
				Name:        content.Body,
				ContentType: content.Info.MimeType,
				Reader:      bytes.NewReader(data),
			}},
		}

		msg, err = sender.Session.ChannelMessageSendComplex(portal.Key.ChannelID, msgSend)
	default:
		portal.log.Warnln("unknown message type:", content.MsgType)
		return
	}

	if err != nil {
		portal.log.Errorfln("Failed to send message: %v", err)

		return
	}

	if msg != nil {
		dbMsg := portal.bridge.DB.Message.New()
		dbMsg.Channel = portal.Key
		dbMsg.DiscordID = msg.ID
		dbMsg.MXID = evt.ID
		dbMsg.SenderID = sender.ID
		// TODO use actual timestamp
		dbMsg.Timestamp = time.Now()
		dbMsg.Insert()
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
	portal.delete()
	portal.cleanup(false)

	// TODO: figure out how to close a dm from the API.

	portal.cleanupIfEmpty()
}

func (portal *Portal) leave(sender *User) {
	if portal.MXID == "" {
		return
	}

	intent := portal.bridge.GetPuppetByID(sender.ID).IntentFor(portal)
	intent.LeaveRoom(portal.MXID)
}

func (portal *Portal) delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByID, portal.Key)

	if portal.MXID != "" {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}

	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) cleanupIfEmpty() {
	users, err := portal.getMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)

		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.delete()
		portal.cleanup(false)
	}
}

func (portal *Portal) cleanup(puppetsOnly bool) {
	if portal.MXID != "" {
		return
	}

	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}

		return
	}

	intent := portal.MainIntent()
	members, err := intent.JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorln("Failed to get portal members for cleanup:", err)

		return
	}

	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}

		puppet := portal.bridge.GetPuppetByMXID(member)
		if portal != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(portal.MXID)
			if err != nil {
				portal.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				portal.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}

	_, err = intent.LeaveRoom(portal.MXID)
	if err != nil {
		portal.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (portal *Portal) getMatrixUsers() ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}

	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != portal.bridge.Bot.UserID {
			users = append(users, userID)
		}
	}

	return users, nil
}

func (portal *Portal) handleMatrixReaction(user *User, evt *event.Event) {
	if user.ID != portal.Key.Receiver {
		return
	}

	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		portal.log.Errorfln("Ignoring reaction %s due to unknown m.relates_to data", evt.ID)

		return
	}

	var discordID string

	msg := portal.bridge.DB.Message.GetByMXID(portal.Key, reaction.RelatesTo.EventID)

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
		attachment := portal.bridge.DB.Attachment.GetByMatrixID(portal.Key, reaction.RelatesTo.EventID)
		discordID = attachment.DiscordMessageID
	} else {
		if msg.DiscordID == "" {
			portal.log.Debugf("Message %s has not yet been sent to discord", reaction.RelatesTo.EventID)

			return
		}

		discordID = msg.DiscordID
	}

	// Figure out if this is a custom emoji or not.
	emojiID := reaction.RelatesTo.Key
	if strings.HasPrefix(emojiID, "mxc://") {
		uri, _ := id.ParseContentURI(emojiID)
		emoji := portal.bridge.DB.Emoji.GetByMatrixURL(uri)
		if emoji == nil {
			portal.log.Errorfln("failed to find emoji for %s", emojiID)

			return
		}

		emojiID = emoji.APIName()
	} else {
		emojiID = variationselector.Remove(emojiID)
	}

	err := user.Session.MessageReactionAdd(portal.Key.ChannelID, discordID, emojiID)
	if err != nil {
		portal.log.Debugf("Failed to send reaction %s id:%s: %v", portal.Key, discordID, err)

		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.Channel = portal.Key
	dbReaction.MessageID = discordID
	dbReaction.Sender = user.ID
	dbReaction.EmojiName = emojiID
	dbReaction.MXID = evt.ID
	dbReaction.Insert()
}

func (portal *Portal) handleDiscordReaction(user *User, reaction *discordgo.MessageReaction, add bool) {
	intent := portal.bridge.GetPuppetByID(reaction.UserID).IntentFor(portal)

	var discordID string
	var matrixReaction string

	if reaction.Emoji.ID != "" {
		dbEmoji := portal.bridge.DB.Emoji.GetByDiscordID(reaction.Emoji.ID)

		if dbEmoji == nil {
			data, mimeType, err := portal.downloadDiscordEmoji(reaction.Emoji.ID, reaction.Emoji.Animated)
			if err != nil {
				portal.log.Warnfln("Failed to download emoji %s from discord: %v", reaction.Emoji.ID, err)

				return
			}

			uri, err := portal.uploadMatrixEmoji(intent, data, mimeType)
			if err != nil {
				portal.log.Warnfln("Failed to upload discord emoji %s to homeserver: %v", reaction.Emoji.ID, err)

				return
			}

			dbEmoji = portal.bridge.DB.Emoji.New()
			dbEmoji.DiscordID = reaction.Emoji.ID
			dbEmoji.DiscordName = reaction.Emoji.Name
			dbEmoji.MatrixURL = uri
			dbEmoji.Insert()
		}

		discordID = dbEmoji.DiscordID
		matrixReaction = dbEmoji.MatrixURL.String()
	} else {
		discordID = reaction.Emoji.Name
		matrixReaction = variationselector.Add(reaction.Emoji.Name)
	}

	// Find the message that we're working with.
	message := portal.bridge.DB.Message.GetByDiscordID(portal.Key, reaction.MessageID)
	if message == nil {
		portal.log.Debugfln("failed to add reaction to message %s: message not found", reaction.MessageID)

		return
	}

	// Lookup an existing reaction
	existing := portal.bridge.DB.Reaction.GetByDiscordID(portal.Key, message.DiscordID, reaction.UserID, discordID)
	if !add {
		if existing == nil {
			portal.log.Debugln("Failed to remove reaction for unknown message", reaction.MessageID)

			return
		}

		_, err := intent.RedactEvent(portal.MXID, existing.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to remove reaction from %s: %v", portal.MXID, err)
		}

		existing.Delete()

		return
	} else if existing != nil {
		portal.log.Debugfln("Ignoring duplicate reaction %s from %s to %s", discordID, reaction.UserID, message.DiscordID)
		return
	}

	content := event.Content{Parsed: &event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			EventID: message.MXID,
			Type:    event.RelAnnotation,
			Key:     matrixReaction,
		},
	}}

	resp, err := intent.Client.SendMessageEvent(portal.MXID, event.EventReaction, &content)
	if err != nil {
		portal.log.Errorfln("failed to send reaction from %s: %v", reaction.MessageID, err)

		return
	}

	if existing == nil {
		dbReaction := portal.bridge.DB.Reaction.New()
		dbReaction.Channel = portal.Key
		dbReaction.MessageID = message.DiscordID
		dbReaction.Sender = reaction.UserID
		dbReaction.EmojiName = discordID
		dbReaction.MXID = resp.EventID
		dbReaction.Insert()
	}
}

func (portal *Portal) handleMatrixRedaction(user *User, evt *event.Event) {
	if user.ID != portal.Key.Receiver {
		return
	}

	// First look if we're redacting a message
	message := portal.bridge.DB.Message.GetByMXID(portal.Key, evt.Redacts)
	if message != nil {
		if message.DiscordID != "" {
			err := user.Session.ChannelMessageDelete(portal.Key.ChannelID, message.DiscordID)
			if err != nil {
				portal.log.Debugfln("Failed to delete discord message %s: %v", message.DiscordID, err)
			} else {
				message.Delete()
			}
		}

		return
	}

	// Now check if it's a reaction.
	reaction := portal.bridge.DB.Reaction.GetByMXID(evt.Redacts)
	if reaction != nil && reaction.Channel == portal.Key {
		err := user.Session.MessageReactionRemove(portal.Key.ChannelID, reaction.MessageID, reaction.EmojiName, reaction.Sender)
		if err != nil {
			portal.log.Debugfln("Failed to delete reaction %s from %s: %v", reaction.EmojiName, reaction.MessageID, err)
		} else {
			reaction.Delete()
		}

		return
	}

	portal.log.Warnfln("Failed to redact %s: no event found", evt.Redacts)
}

func (portal *Portal) update(user *User, channel *discordgo.Channel) {
	name, err := portal.bridge.Config.Bridge.FormatChannelname(channel, user.Session)
	if err != nil {
		portal.log.Warnln("Failed to format channel name, using existing:", err)
	} else {
		portal.Name = name
	}

	intent := portal.MainIntent()

	if portal.Name != name {
		_, err = intent.SetRoomName(portal.MXID, portal.Name)
		if err != nil {
			portal.log.Warnln("Failed to update room name:", err)
		}
	}

	if portal.Topic != channel.Topic {
		portal.Topic = channel.Topic
		_, err = intent.SetRoomTopic(portal.MXID, portal.Topic)
		if err != nil {
			portal.log.Warnln("Failed to update room topic:", err)
		}
	}

	if portal.Avatar != channel.Icon {
		portal.Avatar = channel.Icon

		var url string

		if portal.Type == discordgo.ChannelTypeDM {
			dmUser, err := user.Session.User(portal.OtherUserID)
			if err != nil {
				portal.log.Warnln("failed to lookup the other user in DM", err)
			} else {
				url = dmUser.AvatarURL("")
			}
		} else {
			url = discordgo.EndpointGroupIcon(channel.ID, channel.Icon)
		}

		portal.AvatarURL = id.ContentURI{}
		if url != "" {
			uri, err := uploadAvatar(intent, url)
			if err != nil {
				portal.log.Warnf("failed to upload avatar", err)
			} else {
				portal.AvatarURL = uri
			}
		}

		intent.SetRoomAvatar(portal.MXID, portal.AvatarURL)
	}

	portal.Update()
	portal.log.Debugln("portal updated")
}
