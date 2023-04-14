package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gabriel-vasile/mimetype"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util"
	"maunium.net/go/mautrix/util/variationselector"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/database"
)

type portalDiscordMessage struct {
	msg  interface{}
	user *User

	thread *Thread
}

type portalMatrixMessage struct {
	evt  *event.Event
	user *User
}

var relayClient, _ = discordgo.New("")

type Portal struct {
	*database.Portal

	Parent *Portal
	Guild  *Guild

	bridge *DiscordBridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex

	discordMessages chan portalDiscordMessage
	matrixMessages  chan portalMatrixMessage

	recentMessages *util.RingBuffer[string, *discordgo.Message]

	commands     map[string]*discordgo.ApplicationCommand
	commandsLock sync.RWMutex

	currentlyTyping     []id.UserID
	currentlyTypingLock sync.Mutex
}

const recentMessageBufferSize = 32

var _ bridge.Portal = (*Portal)(nil)
var _ bridge.ReadReceiptHandlingPortal = (*Portal)(nil)
var _ bridge.MembershipHandlingPortal = (*Portal)(nil)
var _ bridge.TypingPortal = (*Portal)(nil)

//var _ bridge.MetaHandlingPortal = (*Portal)(nil)
//var _ bridge.DisappearingPortal = (*Portal)(nil)

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update()
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser || portal.RelayWebhookID != "" {
		portal.matrixMessages <- portalMatrixMessage{user: user.(*User), evt: evt}
	}
}

var (
	portalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
)

func (br *DiscordBridge) loadPortal(dbPortal *database.Portal, key *database.PortalKey, chanType discordgo.ChannelType) *Portal {
	if dbPortal == nil {
		if key == nil || chanType < 0 {
			return nil
		}

		dbPortal = br.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Type = chanType
		dbPortal.Insert()
	}

	portal := br.NewPortal(dbPortal)

	br.portalsByID[portal.Key] = portal
	if portal.MXID != "" {
		br.portalsByMXID[portal.MXID] = portal
	}

	if portal.GuildID != "" {
		portal.Guild = portal.bridge.GetGuildByID(portal.GuildID, true)
	}
	if portal.ParentID != "" {
		parentKey := database.NewPortalKey(portal.ParentID, "")
		var ok bool
		portal.Parent, ok = br.portalsByID[parentKey]
		if !ok {
			portal.Parent = br.loadPortal(br.DB.Portal.GetByID(parentKey), nil, -1)
		}
	}

	return portal
}

func (br *DiscordBridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByMXID(mxid), nil, -1)
	}

	return portal
}

func (user *User) GetPortalByMeta(meta *discordgo.Channel) *Portal {
	return user.GetPortalByID(meta.ID, meta.Type)
}

func (user *User) GetExistingPortalByID(id string) *Portal {
	return user.bridge.GetExistingPortalByID(database.NewPortalKey(id, user.DiscordID))
}

func (user *User) GetPortalByID(id string, chanType discordgo.ChannelType) *Portal {
	return user.bridge.GetPortalByID(database.NewPortalKey(id, user.DiscordID), chanType)
}

func (br *DiscordBridge) GetExistingPortalByID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByID[key]
	if !ok {
		if key.Receiver != "" {
			portal, ok = br.portalsByID[database.NewPortalKey(key.ChannelID, "")]
		}
		if !ok {
			return br.loadPortal(br.DB.Portal.GetByID(key), nil, -1)
		}
	}

	return portal
}

func (br *DiscordBridge) GetPortalByID(key database.PortalKey, chanType discordgo.ChannelType) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	if chanType != discordgo.ChannelTypeDM {
		key.Receiver = ""
	}

	portal, ok := br.portalsByID[key]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByID(key), &key, chanType)
	}

	return portal
}

func (br *DiscordBridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll())
}

func (br *DiscordBridge) GetAllPortalsInGuild(guildID string) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllInGuild(guildID))
}

func (br *DiscordBridge) GetAllIPortals() (iportals []bridge.Portal) {
	portals := br.GetAllPortals()
	iportals = make([]bridge.Portal, len(portals))
	for i, portal := range portals {
		iportals[i] = portal
	}
	return iportals
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
			portal = br.loadPortal(dbPortal, nil, -1)
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

		recentMessages: util.NewRingBuffer[string, *discordgo.Message](recentMessageBufferSize),

		commands: make(map[string]*discordgo.ApplicationCommand),
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

type CustomBridgeInfoContent struct {
	event.BridgeEventContent
	RoomType string `json:"com.beeper.room_type,omitempty"`
}

func init() {
	event.TypeMap[event.StateBridge] = reflect.TypeOf(CustomBridgeInfoContent{})
	event.TypeMap[event.StateHalfShotBridge] = reflect.TypeOf(CustomBridgeInfoContent{})
}

func (portal *Portal) getBridgeInfo() (string, CustomBridgeInfoContent) {
	bridgeInfo := event.BridgeEventContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: event.BridgeInfoSection{
			ID:          "discordgo",
			DisplayName: "Discord",
			AvatarURL:   portal.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
			ExternalURL: "https://discord.com/",
		},
		Channel: event.BridgeInfoSection{
			ID:          portal.Key.ChannelID,
			DisplayName: portal.Name,
		},
	}
	var bridgeInfoStateKey string
	if portal.GuildID == "" {
		bridgeInfoStateKey = fmt.Sprintf("fi.mau.discord://discord/dm/%s", portal.Key.ChannelID)
		bridgeInfo.Channel.ExternalURL = fmt.Sprintf("https://discord.com/channels/@me/%s", portal.Key.ChannelID)
	} else {
		bridgeInfo.Network = &event.BridgeInfoSection{
			ID: portal.GuildID,
		}
		if portal.Guild != nil {
			bridgeInfo.Network.DisplayName = portal.Guild.Name
			bridgeInfo.Network.AvatarURL = portal.Guild.AvatarURL.CUString()
			// TODO is it possible to find the URL?
		}
		bridgeInfoStateKey = fmt.Sprintf("fi.mau.discord://discord/%s/%s", portal.GuildID, portal.Key.ChannelID)
		bridgeInfo.Channel.ExternalURL = fmt.Sprintf("https://discord.com/channels/%s/%s", portal.GuildID, portal.Key.ChannelID)
	}
	var roomType string
	if portal.Type == discordgo.ChannelTypeDM || portal.Type == discordgo.ChannelTypeGroupDM {
		roomType = "dm"
	}
	return bridgeInfoStateKey, CustomBridgeInfoContent{bridgeInfo, roomType}
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (portal *Portal) shouldSetDMRoomMetadata() bool {
	return !portal.IsPrivateChat() ||
		portal.bridge.Config.Bridge.PrivateChatPortalMeta == "always" ||
		(portal.IsEncrypted() && portal.bridge.Config.Bridge.PrivateChatPortalMeta != "never")
}

func (portal *Portal) GetEncryptionEventContent() (evt *event.EncryptionEventContent) {
	evt = &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if rot := portal.bridge.Config.Bridge.Encryption.Rotation; rot.EnableCustom {
		evt.RotationPeriodMillis = rot.Milliseconds
		evt.RotationPeriodMessages = rot.Messages
	}
	return
}

func (portal *Portal) CreateMatrixRoom(user *User, channel *discordgo.Channel) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if portal.MXID != "" {
		return nil
	}
	portal.log.Infoln("Creating Matrix room for channel")

	channel = portal.UpdateInfo(user, channel)
	if channel == nil {
		return fmt.Errorf("didn't find channel metadata")
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()
	initialState := []*event.Event{{
		Type:     event.StateBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     event.StateHalfShotBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}}

	var invite []id.UserID

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: portal.GetEncryptionEventContent(),
			},
		})
		portal.Encrypted = true

		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
		}
	}

	if !portal.AvatarURL.IsEmpty() && portal.shouldSetDMRoomMetadata() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{Parsed: &event.RoomAvatarEventContent{
				URL: portal.AvatarURL,
			}},
		})
		portal.AvatarSet = true
	} else {
		portal.AvatarSet = false
	}

	creationContent := make(map[string]interface{})
	if portal.Type == discordgo.ChannelTypeGuildCategory {
		creationContent["type"] = event.RoomTypeSpace
	}
	if !portal.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}
	spaceID := portal.ExpectedSpaceID()
	if spaceID != "" {
		spaceIDStr := spaceID.String()
		initialState = append(initialState, &event.Event{
			Type:     event.StateSpaceParent,
			StateKey: &spaceIDStr,
			Content: event.Content{Parsed: &event.SpaceParentEventContent{
				Via:       []string{portal.bridge.AS.HomeserverDomain},
				Canonical: true,
			}},
		})
	}
	if portal.bridge.Config.Bridge.RestrictedRooms && portal.Guild != nil && portal.Guild.MXID != "" {
		// TODO don't do this for private channels in guilds
		initialState = append(initialState, &event.Event{
			Type: event.StateJoinRules,
			Content: event.Content{Parsed: &event.JoinRulesEventContent{
				JoinRule: event.JoinRuleRestricted,
				Allow: []event.JoinRuleAllow{{
					RoomID: spaceID,
					Type:   event.JoinRuleAllowRoomMembership,
				}},
			}},
		})
	}

	req := &mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            portal.Name,
		Topic:           portal.Topic,
		Invite:          invite,
		Preset:          "private_chat",
		IsDirect:        portal.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,
	}
	if !portal.shouldSetDMRoomMetadata() {
		req.Name = ""
	}
	resp, err := intent.CreateRoom(req)
	if err != nil {
		portal.log.Warnln("Failed to create room:", err)
		return err
	}

	portal.NameSet = len(req.Name) > 0
	portal.TopicSet = len(req.Topic) > 0
	portal.MXID = resp.RoomID
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	portal.Update()
	portal.log.Infoln("Matrix room created:", portal.MXID)

	if portal.Encrypted && portal.IsPrivateChat() {
		err = portal.bridge.Bot.EnsureJoined(portal.MXID, appservice.EnsureJoinedParams{BotOverride: portal.MainIntent().Client})
		if err != nil {
			portal.log.Errorfln("Failed to ensure bridge bot is joined to private chat portal: %v", err)
		}
	}

	if portal.GuildID == "" {
		user.addPrivateChannelToSpace(portal)
	} else {
		portal.updateSpace()
	}
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
		_, ok := msg.msg.(*discordgo.MessageCreate)
		if !ok {
			portal.log.Warnln("Can't create Matrix room from non new message event")
			return
		}

		portal.log.Debugln("Creating Matrix room from incoming message")
		if err := portal.CreateMatrixRoom(msg.user, nil); err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	}

	switch convertedMsg := msg.msg.(type) {
	case *discordgo.MessageCreate:
		portal.handleDiscordMessageCreate(msg.user, convertedMsg.Message, msg.thread)
	case *discordgo.MessageUpdate:
		portal.handleDiscordMessageUpdate(msg.user, convertedMsg.Message)
	case *discordgo.MessageDelete:
		portal.handleDiscordMessageDelete(msg.user, convertedMsg.Message)
	case *discordgo.MessageReactionAdd:
		portal.handleDiscordReaction(msg.user, convertedMsg.MessageReaction, true, msg.thread, convertedMsg.Member)
	case *discordgo.MessageReactionRemove:
		portal.handleDiscordReaction(msg.user, convertedMsg.MessageReaction, false, msg.thread, nil)
	default:
		portal.log.Warnln("unknown message type")
	}
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) markMessageHandled(discordID string, editIndex int, authorID string, timestamp time.Time, threadID string, parts []database.MessagePart) {
	msg := portal.bridge.DB.Message.New()
	msg.Channel = portal.Key
	msg.DiscordID = discordID
	msg.EditIndex = editIndex
	msg.SenderID = authorID
	msg.Timestamp = timestamp
	msg.ThreadID = threadID
	msg.MassInsert(parts)
}

func (portal *Portal) handleDiscordMessageCreate(user *User, msg *discordgo.Message, thread *Thread) {
	switch msg.Type {
	case discordgo.MessageTypeChannelNameChange, discordgo.MessageTypeChannelIconChange, discordgo.MessageTypeChannelPinnedMessage:
		// These are handled via channel updates
		return
	}

	portal.recentMessages.Push(msg.ID, msg)

	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing != nil {
		portal.log.Debugln("Dropping duplicate message", msg.ID)
		return
	}
	portal.log.Debugfln("Starting handling of %s by %s", msg.ID, msg.Author.ID)

	for _, mention := range msg.Mentions {
		puppet := portal.bridge.GetPuppetByID(mention.ID)
		puppet.UpdateInfo(nil, mention)
	}

	puppet := portal.bridge.GetPuppetByID(msg.Author.ID)
	puppet.UpdateInfo(user, msg.Author)
	intent := puppet.IntentFor(portal)

	var discordThreadID string
	var threadRootEvent, lastThreadEvent, replyToEvent id.EventID
	if thread != nil {
		discordThreadID = thread.ID
		threadRootEvent = thread.RootMXID
		lastThreadEvent = threadRootEvent
		lastInThread := portal.bridge.DB.Message.GetLastInThread(portal.Key, thread.ID)
		if lastInThread != nil {
			lastThreadEvent = lastInThread.MXID
		}
	}

	if msg.MessageReference != nil {
		// This could be used to find cross-channel replies, but Matrix doesn't support those currently.
		//key := database.PortalKey{msg.MessageReference.ChannelID, user.ID}
		replyToMsg := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.MessageReference.MessageID)
		if len(replyToMsg) > 0 {
			replyToEvent = replyToMsg[0].MXID
		}
	}

	ts, _ := discordgo.SnowflakeTimestamp(msg.ID)
	parts := portal.convertDiscordMessage(intent, msg)
	dbParts := make([]database.MessagePart, 0, len(parts))
	for i, part := range parts {
		if (replyToEvent != "" || threadRootEvent != "") && part.Content.RelatesTo == nil {
			part.Content.RelatesTo = &event.RelatesTo{}
		}
		if threadRootEvent != "" {
			part.Content.RelatesTo.SetThread(threadRootEvent, lastThreadEvent)
		}
		if replyToEvent != "" {
			part.Content.RelatesTo.SetReplyTo(replyToEvent)
			// Only set reply for first event
			replyToEvent = ""
		}
		resp, err := portal.sendMatrixMessage(intent, part.Type, part.Content, part.Extra, ts.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Failed to send part #%d (attachment ID %q) of message %s to Matrix: %v", i+1, part.AttachmentID, msg.ID, err)
			continue
		}
		lastThreadEvent = resp.EventID
		dbParts = append(dbParts, database.MessagePart{AttachmentID: part.AttachmentID, MXID: resp.EventID})
	}
	if len(parts) == 0 {
		portal.log.Warnfln("Unhandled message %s (type %d)", msg.ID, msg.Type)
	} else if len(dbParts) == 0 {
		portal.log.Warnfln("All parts of message %s failed to send to Matrix", msg.ID)
	} else {
		portal.markMessageHandled(msg.ID, 0, msg.Author.ID, ts, discordThreadID, dbParts)
	}
}

const JoinThreadReaction = "join thread"

func (portal *Portal) sendThreadCreationNotice(thread *Thread) {
	thread.creationNoticeLock.Lock()
	defer thread.creationNoticeLock.Unlock()
	if thread.CreationNoticeMXID != "" {
		return
	}
	creationNotice := "Thread created. React to this message with \"join thread\" to join the thread on Discord."
	if portal.bridge.Config.Bridge.AutojoinThreadOnOpen {
		creationNotice = "Thread created. Opening this thread will auto-join you to it on Discord."
	}
	resp, err := portal.sendMatrixMessage(portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
		Body:      creationNotice,
		MsgType:   event.MsgNotice,
		RelatesTo: (&event.RelatesTo{}).SetThread(thread.RootMXID, thread.RootMXID),
	}, nil, time.Now().UnixMilli())
	if err != nil {
		portal.log.Errorfln("Failed to send thread creation notice: %v", err)
		return
	}
	portal.bridge.threadsLock.Lock()
	thread.CreationNoticeMXID = resp.EventID
	portal.bridge.threadsByCreationNoticeMXID[resp.EventID] = thread
	portal.bridge.threadsLock.Unlock()
	thread.Update()
	portal.log.Debugfln("Sent notice %s about thread for %s being created", thread.CreationNoticeMXID, thread.ID)

	resp, err = portal.MainIntent().SendMessageEvent(portal.MXID, event.EventReaction, &event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: thread.CreationNoticeMXID,
			Key:     JoinThreadReaction,
		},
	})
	if err != nil {
		portal.log.Errorfln("Failed to send prefilled reaction to thread creation notice: %v", err)
	} else {
		portal.log.Debugfln("Sent prefilled reaction %s to thread creation notice %s", resp.EventID, thread.CreationNoticeMXID)
	}
}

func (portal *Portal) handleDiscordMessageUpdate(user *User, msg *discordgo.Message) {
	if portal.MXID == "" {
		portal.log.Warnln("handle message called without a valid portal")
		return
	}

	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing == nil {
		portal.log.Warnfln("Dropping update of unknown message %s", msg.ID)
		return
	}

	if msg.Flags == discordgo.MessageFlagsHasThread {
		thread := portal.bridge.GetThreadByID(msg.ID, existing[0])
		portal.log.Debugfln("Marked %s as a thread root", msg.ID)
		if thread.CreationNoticeMXID == "" {
			portal.sendThreadCreationNotice(thread)
		}
	}

	if msg.Author == nil {
		creationMessage, ok := portal.recentMessages.Get(msg.ID)
		if !ok {
			portal.log.Debugfln("Dropping edit with no author of non-recent message %s", msg.ID)
			return
		} else if creationMessage.Type == discordgo.MessageTypeCall {
			portal.log.Debugfln("Dropping edit of call message %s", msg.ID)
			return
		}
		portal.log.Debugfln("Found original message %s in cache for edit without author", msg.ID)
		if len(msg.Embeds) > 0 {
			creationMessage.Embeds = msg.Embeds
		}
		if len(msg.Attachments) > 0 {
			creationMessage.Attachments = msg.Attachments
		}
		if len(msg.Components) > 0 {
			creationMessage.Components = msg.Components
		}
		// TODO are there other fields that need copying?
		msg = creationMessage
	} else {
		portal.recentMessages.Replace(msg.ID, msg)
	}

	intent := portal.bridge.GetPuppetByID(msg.Author.ID).IntentFor(portal)

	attachmentMap := map[string]*database.Message{}
	for _, existingPart := range existing {
		if existingPart.AttachmentID != "" {
			attachmentMap[existingPart.AttachmentID] = existingPart
		}
	}
	for _, remainingAttachment := range msg.Attachments {
		if _, found := attachmentMap[remainingAttachment.ID]; found {
			delete(attachmentMap, remainingAttachment.ID)
		}
	}
	for _, remainingSticker := range msg.StickerItems {
		if _, found := attachmentMap[remainingSticker.ID]; found {
			delete(attachmentMap, remainingSticker.ID)
		}
	}
	for _, remainingEmbed := range msg.Embeds {
		// Other types of embeds are sent inline with the text message part
		if getEmbedType(remainingEmbed) != EmbedVideo {
			continue
		}
		embedID := "video_" + remainingEmbed.URL
		if _, found := attachmentMap[embedID]; found {
			delete(attachmentMap, embedID)
		}
	}
	for _, deletedAttachment := range attachmentMap {
		_, err := intent.RedactEvent(portal.MXID, deletedAttachment.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to remove attachment %s: %v", deletedAttachment.MXID, err)
		}
		deletedAttachment.Delete()
	}

	var converted *ConvertedMessage
	// Slightly hacky special case: messages with gif links will get an embed with the gif.
	// The link isn't rendered on Discord, so just edit the link message into a gif message on Matrix too.
	if isPlainGifMessage(msg) {
		converted = portal.convertDiscordVideoEmbed(intent, msg.Embeds[0])
	} else {
		converted = portal.convertDiscordTextMessage(intent, msg)
	}
	if converted == nil {
		portal.log.Debugfln("Dropping non-text edit to %s (message on matrix: %t, text on discord: %t)", msg.ID, existing[0].AttachmentID == "", len(msg.Content) > 0)
		return
	}
	converted.Content.SetEdit(existing[0].MXID)
	if converted.Extra != nil {
		converted.Extra = map[string]any{
			"m.new_content": converted.Extra,
		}
	}

	var editTS int64
	if msg.EditedTimestamp != nil {
		editTS = msg.EditedTimestamp.UnixMilli()
	}
	// TODO figure out some way to deduplicate outgoing edits
	resp, err := portal.sendMatrixMessage(intent, event.EventMessage, converted.Content, converted.Extra, editTS)
	if err != nil {
		portal.log.Warnfln("Failed to send message %s to matrix: %v", msg.ID, err)
		return
	}

	portal.sendDeliveryReceipt(resp.EventID)

	//ts, _ := msg.Timestamp.Parse()
	//portal.markMessageHandled(existing, msg.ID, resp.EventID, msg.Author.ID, ts)
}

func (portal *Portal) handleDiscordMessageDelete(user *User, msg *discordgo.Message) {
	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	intent := portal.MainIntent()
	var lastResp id.EventID
	for _, dbMsg := range existing {
		resp, err := intent.RedactEvent(portal.MXID, dbMsg.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to redact message %s: %v", dbMsg.MXID, err)
		} else if resp != nil && resp.EventID != "" {
			lastResp = resp.EventID
		}
		dbMsg.Delete()
	}
	if lastResp != "" {
		portal.sendDeliveryReceipt(lastResp)
	}
}

func (portal *Portal) handleDiscordTyping(evt *discordgo.TypingStart) {
	puppet := portal.bridge.GetPuppetByID(evt.UserID)
	if puppet.Name == "" {
		// Puppet hasn't been synced yet
		return
	}
	intent := puppet.IntentFor(portal)
	err := intent.EnsureJoined(portal.MXID)
	if err != nil {
		portal.log.Warnfln("Failed to ensure %s is joined for typing notification: %v", puppet.MXID, portal.MXID, err)
		return
	}
	_, err = intent.UserTyping(portal.MXID, true, 12*time.Second)
	if err != nil {
		portal.log.Warnfln("Failed to mark %s as typing: %v", puppet.MXID, portal.MXID, err)
	}
}

func (portal *Portal) syncParticipants(source *User, participants []*discordgo.User) {
	for _, participant := range participants {
		puppet := portal.bridge.GetPuppetByID(participant.ID)
		puppet.UpdateInfo(source, participant)

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

func (portal *Portal) encrypt(intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, content)
	portal.encryptLock.Unlock()
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

func (portal *Portal) sendMatrixMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	var err error
	eventType, err = portal.encrypt(intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
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
	case event.EventMessage, event.EventSticker:
		portal.handleMatrixMessage(msg.user, msg.evt)
	case event.EventRedaction:
		portal.handleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(msg.user, msg.evt)
	default:
		portal.log.Debugln("unknown event type", msg.evt.Type)
	}
}

const discordEpoch = 1420070400000

func generateNonce() string {
	snowflake := (time.Now().UnixMilli() - discordEpoch) << 22
	// Nonce snowflakes don't have internal IDs or increments
	return strconv.FormatInt(snowflake, 10)
}

func (portal *Portal) getEvent(mxid id.EventID) (*event.Event, error) {
	evt, err := portal.MainIntent().GetEvent(portal.MXID, mxid)
	if err != nil {
		return nil, err
	}
	_ = evt.Content.ParseRaw(evt.Type)
	if evt.Type == event.EventEncrypted {
		decryptedEvt, err := portal.bridge.Crypto.Decrypt(evt)
		if err != nil {
			return nil, err
		} else {
			evt = decryptedEvt
		}
	}
	return evt, nil
}

func genThreadName(evt *event.Event) string {
	body := evt.Content.AsMessage().Body
	if len(body) == 0 {
		return "thread"
	}
	fields := strings.Fields(body)
	var title string
	for _, field := range fields {
		if len(title)+len(field) < 40 {
			title += field
			title += " "
			continue
		}
		if len(title) == 0 {
			title = field[:40]
		}
		break
	}
	return title
}

func (portal *Portal) startThreadFromMatrix(sender *User, threadRoot id.EventID) (string, error) {
	rootEvt, err := portal.getEvent(threadRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get root event: %w", err)
	}
	threadName := genThreadName(rootEvt)

	existingMsg := portal.bridge.DB.Message.GetByMXID(portal.Key, threadRoot)
	if existingMsg == nil {
		return "", fmt.Errorf("unknown root event")
	} else if existingMsg.ThreadID != "" {
		return "", fmt.Errorf("root event is already in a thread")
	} else {
		var ch *discordgo.Channel
		ch, err = sender.Session.MessageThreadStartComplex(portal.Key.ChannelID, existingMsg.DiscordID, &discordgo.ThreadStart{
			Name:                threadName,
			AutoArchiveDuration: 24 * 60,
			Type:                discordgo.ChannelTypeGuildPublicThread,
			Location:            "Message",
		})
		if err != nil {
			return "", fmt.Errorf("error starting thread: %v", err)
		}
		portal.log.Debugfln("Created Discord thread from %s/%s", threadRoot, ch.ID)
		portal.bridge.GetThreadByID(existingMsg.DiscordID, existingMsg)
		return ch.ID, nil
	}
}

func (portal *Portal) sendErrorMessage(msgType, message string, confirmed bool) id.EventID {
	if !portal.bridge.Config.Bridge.MessageErrorNotices {
		return ""
	}
	certainty := "may not have been"
	if confirmed {
		certainty = "was not"
	}
	resp, err := portal.sendMatrixMessage(portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("\u26a0 Your %s %s bridged: %v", msgType, certainty, message),
	}, nil, 0)
	if err != nil {
		portal.log.Warnfln("Failed to send bridging error message:", err)
		return ""
	}
	return resp.EventID
}

var (
	errUnknownMsgType              = errors.New("unknown msgtype")
	errUnexpectedParsedContentType = errors.New("unexpected parsed content type")
	errUserNotReceiver             = errors.New("user is not portal receiver")
	errUserNotLoggedIn             = errors.New("user is not logged in and portal doesn't have webhook")
	errUnknownEditTarget           = errors.New("unknown edit target")
	errUnknownRelationType         = errors.New("unknown relation type")
	errTargetNotFound              = errors.New("target event not found")
	errUnknownEmoji                = errors.New("unknown emoji")
	errCantStartThread             = errors.New("can't create thread without being logged into Discord")
)

func errorToStatusReason(err error) (reason event.MessageStatusReason, status event.MessageStatus, isCertain, sendNotice bool, humanMessage string) {
	switch {
	case errors.Is(err, errUnknownMsgType),
		errors.Is(err, errUnknownRelationType),
		errors.Is(err, errUnexpectedParsedContentType),
		errors.Is(err, errUnknownEmoji),
		errors.Is(err, id.InvalidContentURI),
		errors.Is(err, attachment.UnsupportedVersion),
		errors.Is(err, attachment.UnsupportedAlgorithm),
		errors.Is(err, errCantStartThread):
		return event.MessageStatusUnsupported, event.MessageStatusFail, true, true, ""
	case errors.Is(err, attachment.HashMismatch),
		errors.Is(err, attachment.InvalidKey),
		errors.Is(err, attachment.InvalidInitVector):
		return event.MessageStatusUndecryptable, event.MessageStatusFail, true, true, ""
	case errors.Is(err, errUserNotReceiver), errors.Is(err, errUserNotLoggedIn):
		return event.MessageStatusNoPermission, event.MessageStatusFail, true, false, ""
	case errors.Is(err, errUnknownEditTarget):
		return event.MessageStatusGenericError, event.MessageStatusFail, true, false, ""
	case errors.Is(err, errTargetNotFound):
		return event.MessageStatusGenericError, event.MessageStatusFail, true, false, ""
	default:
		return event.MessageStatusGenericError, event.MessageStatusRetriable, false, true, ""
	}
}

func (portal *Portal) sendStatusEvent(evtID id.EventID, err error) {
	if !portal.bridge.Config.Bridge.MessageStatusEvents {
		return
	}
	intent := portal.bridge.Bot
	if !portal.Encrypted {
		// Bridge bot isn't present in unencrypted DMs
		intent = portal.MainIntent()
	}
	stateKey, _ := portal.getBridgeInfo()
	content := event.BeeperMessageStatusEventContent{
		Network: stateKey,
		RelatesTo: event.RelatesTo{
			Type:    event.RelReference,
			EventID: evtID,
		},
		Status: event.MessageStatusSuccess,
	}
	if err == nil {
		content.Status = event.MessageStatusSuccess
	} else {
		content.Reason, content.Status, _, _, content.Message = errorToStatusReason(err)
		content.Error = err.Error()
	}
	_, err = intent.SendMessageEvent(portal.MXID, event.BeeperMessageStatus, &content)
	if err != nil {
		portal.log.Warnln("Failed to send message status event:", err)
	}
}

func (portal *Portal) sendMessageMetrics(evt *event.Event, err error, part string) {
	var msgType string
	switch evt.Type {
	case event.EventMessage, event.EventSticker:
		msgType = "message"
	case event.EventReaction:
		msgType = "reaction"
	case event.EventRedaction:
		msgType = "redaction"
	default:
		msgType = "unknown event"
	}
	evtDescription := evt.ID.String()
	if evt.Type == event.EventRedaction {
		evtDescription += fmt.Sprintf(" of %s", evt.Redacts)
	}
	if err != nil {
		level := log.LevelError
		if part == "Ignoring" {
			level = log.LevelDebug
		}
		portal.log.Logfln(level, "%s %s %s from %s: %v", part, msgType, evtDescription, evt.Sender, err)
		reason, statusCode, isCertain, sendNotice, _ := errorToStatusReason(err)
		checkpointStatus := status.ReasonToCheckpointStatus(reason, statusCode)
		portal.bridge.SendMessageCheckpoint(evt, status.MsgStepRemote, err, checkpointStatus, 0)
		if sendNotice {
			portal.sendErrorMessage(msgType, err.Error(), isCertain)
		}
		portal.sendStatusEvent(evt.ID, err)
	} else {
		portal.log.Debugfln("Handled Matrix %s %s", msgType, evtDescription)
		portal.sendDeliveryReceipt(evt.ID)
		portal.bridge.SendMessageSuccessCheckpoint(evt, status.MsgStepRemote, 0)
		portal.sendStatusEvent(evt.ID, nil)
	}
}

func (portal *Portal) getRelayUserMeta(sender *User) (name, avatarURL string) {
	member := portal.bridge.StateStore.GetMember(portal.MXID, sender.MXID)
	name = member.Displayname
	if name == "" {
		name = sender.MXID.String()
	}
	mxc := member.AvatarURL.ParseOrIgnore()
	if !mxc.IsEmpty() {
		avatarURL = mautrix.BuildURL(
			portal.bridge.PublicHSAddress,
			"_matrix", "media", "v3", "download", mxc.Homeserver, mxc.FileID,
		).String()
	}
	return
}

func (portal *Portal) handleMatrixMessage(sender *User, evt *event.Event) {
	if portal.IsPrivateChat() && sender.DiscordID != portal.Key.Receiver {
		go portal.sendMessageMetrics(evt, errUserNotReceiver, "Ignoring")
		return
	}

	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed), "Ignoring")
		return
	}

	channelID := portal.Key.ChannelID
	sess := sender.Session
	if sess == nil && portal.RelayWebhookID == "" {
		go portal.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring")
		return
	}
	var threadID string

	if editMXID := content.GetRelatesTo().GetReplaceID(); editMXID != "" && content.NewContent != nil {
		edits := portal.bridge.DB.Message.GetByMXID(portal.Key, editMXID)
		if edits != nil {
			discordContent, allowedMentions := portal.parseMatrixHTML(content.NewContent)
			var err error
			if sess != nil {
				// TODO save edit in message table
				_, err = sess.ChannelMessageEdit(edits.DiscordProtoChannelID(), edits.DiscordID, discordContent)
			} else {
				_, err = relayClient.WebhookMessageEdit(portal.RelayWebhookID, portal.RelayWebhookSecret, edits.DiscordID, &discordgo.WebhookEdit{
					Content:         &discordContent,
					AllowedMentions: allowedMentions,
				})
			}
			go portal.sendMessageMetrics(evt, err, "Failed to edit")
		} else {
			go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errUnknownEditTarget, editMXID), "Ignoring")
		}
		return
	} else if threadRoot := content.GetRelatesTo().GetThreadParent(); threadRoot != "" {
		existingThread := portal.bridge.DB.Thread.GetByMatrixRootMsg(threadRoot)
		if existingThread != nil {
			threadID = existingThread.ID
		} else {
			if sess == nil {
				// TODO start thread with bot?
				go portal.sendMessageMetrics(evt, errCantStartThread, "Dropping")
				return
			}
			var err error
			threadID, err = portal.startThreadFromMatrix(sender, threadRoot)
			if err != nil {
				portal.log.Warnfln("Failed to start thread from %s: %v", threadRoot, err)
			}
		}
	}
	if threadID != "" {
		channelID = threadID
	}

	var sendReq discordgo.MessageSend

	var description string
	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
		if mimeData := mimetype.Lookup(content.Info.MimeType); mimeData != nil {
			description = content.Body
			content.Body = "sticker" + mimeData.Extension()
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		if replyToMXID := content.RelatesTo.GetNonFallbackReplyTo(); replyToMXID != "" {
			replyTo := portal.bridge.DB.Message.GetByMXID(portal.Key, replyToMXID)
			if replyTo != nil && replyTo.ThreadID == threadID {
				sendReq.Reference = &discordgo.MessageReference{
					ChannelID: channelID,
					MessageID: replyTo.DiscordID,
				}
			}
		}
		sendReq.Content, sendReq.AllowedMentions = portal.parseMatrixHTML(content)
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := downloadMatrixAttachment(portal.MainIntent(), content)
		if err != nil {
			go portal.sendMessageMetrics(evt, err, "Error downloading media in")
			return
		}
		filename := content.Body
		if content.FileName != "" && content.FileName != content.Body {
			filename = content.FileName
			sendReq.Content, sendReq.AllowedMentions = portal.parseMatrixHTML(content)
		}

		if sess != nil && sess.IsUser {
			att := &discordgo.MessageAttachment{
				ID:          "0",
				Filename:    filename,
				Description: description,
			}
			sendReq.Attachments = []*discordgo.MessageAttachment{att}
			prep, err := sender.Session.ChannelAttachmentCreate(channelID, &discordgo.ReqPrepareAttachments{
				Files: []*discordgo.FilePrepare{{
					Size: len(data),
					Name: att.Filename,
					ID:   sender.NextDiscordUploadID(),
				}},
			})
			if err != nil {
				go portal.sendMessageMetrics(evt, err, "Error preparing to reupload media in")
				return
			}
			prepared := prep.Attachments[0]
			att.UploadedFilename = prepared.UploadFilename
			err = uploadDiscordAttachment(prepared.UploadURL, data)
			if err != nil {
				go portal.sendMessageMetrics(evt, err, "Error reuploading media in")
				return
			}
		} else {
			sendReq.Files = []*discordgo.File{{
				Name:        filename,
				ContentType: content.Info.MimeType,
				Reader:      bytes.NewReader(data),
			}}
		}
	default:
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %q", errUnknownMsgType, content.MsgType), "Ignoring")
		return
	}
	if sess != nil {
		// AllowedMentions must not be set for real users, and it's also not that useful for personal bots.
		// It's only important for relaying, where the webhook may have higher permissions than the user on Matrix.
		sendReq.AllowedMentions = nil
	} else if strings.Contains(sendReq.Content, "@everyone") || strings.Contains(sendReq.Content, "@here") {
		powerLevels, err := portal.MainIntent().PowerLevels(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to get power levels in %s to check if %s can @everyone: %v", portal.MXID, sender.MXID, err)
		} else if powerLevels.GetUserLevel(sender.MXID) >= powerLevels.Notifications.Room() {
			sendReq.AllowedMentions.Parse = append(sendReq.AllowedMentions.Parse, discordgo.AllowedMentionTypeEveryone)
		}
	}
	sendReq.Nonce = generateNonce()
	var msg *discordgo.Message
	var err error
	if sess != nil {
		msg, err = sess.ChannelMessageSendComplex(channelID, &sendReq)
	} else {
		username, avatarURL := portal.getRelayUserMeta(sender)
		msg, err = relayClient.WebhookThreadExecute(portal.RelayWebhookID, portal.RelayWebhookSecret, true, threadID, &discordgo.WebhookParams{
			Content:         sendReq.Content,
			Username:        username,
			AvatarURL:       avatarURL,
			TTS:             sendReq.TTS,
			Files:           sendReq.Files,
			Components:      sendReq.Components,
			Embeds:          sendReq.Embeds,
			AllowedMentions: sendReq.AllowedMentions,
		})
	}
	go portal.sendMessageMetrics(evt, err, "Error sending")
	if msg != nil {
		dbMsg := portal.bridge.DB.Message.New()
		dbMsg.Channel = portal.Key
		dbMsg.DiscordID = msg.ID
		if len(msg.Attachments) > 0 {
			dbMsg.AttachmentID = msg.Attachments[0].ID
		}
		dbMsg.MXID = evt.ID
		if sess != nil {
			dbMsg.SenderID = sender.DiscordID
		} else {
			dbMsg.SenderID = portal.RelayWebhookID
		}
		dbMsg.Timestamp, _ = discordgo.SnowflakeTimestamp(msg.ID)
		dbMsg.ThreadID = threadID
		dbMsg.Insert()
	}
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Debugfln("Failed to send delivery receipt for %s: %v", eventID, err)
		}
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	sender := brSender.(*User)
	if portal.IsPrivateChat() && sender.DiscordID == portal.Key.Receiver {
		portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
		portal.cleanup(false)
		portal.RemoveMXID()
	} else {
		portal.cleanupIfEmpty()
	}
}

func (portal *Portal) HandleMatrixKick(brSender bridge.User, brTarget bridge.Ghost)   {}
func (portal *Portal) HandleMatrixInvite(brSender bridge.User, brTarget bridge.Ghost) {}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByID, portal.Key)
	if portal.MXID != "" {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) cleanupIfEmpty() {
	if portal.MXID == "" {
		return
	}

	users, err := portal.getMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)
		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.cleanup(false)
		portal.RemoveMXID()
	}
}

func (portal *Portal) RemoveMXID() {
	portal.bridge.portalsLock.Lock()
	defer portal.bridge.portalsLock.Unlock()
	if portal.MXID == "" {
		return
	}
	delete(portal.bridge.portalsByMXID, portal.MXID)
	portal.MXID = ""
	portal.AvatarSet = false
	portal.NameSet = false
	portal.TopicSet = false
	portal.Encrypted = false
	portal.InSpace = ""
	portal.FirstEventID = ""
	portal.Update()
	portal.bridge.DB.Message.DeleteAll(portal.Key)
}

func (portal *Portal) cleanup(puppetsOnly bool) {
	if portal.MXID == "" {
		return
	}
	intent := portal.MainIntent()
	if portal.bridge.SpecVersions.UnstableFeatures["com.beeper.room_yeeting"] {
		err := intent.BeeperDeleteRoom(portal.MXID)
		if err != nil && !errors.Is(err, mautrix.MNotFound) {
			portal.log.Errorfln("Failed to delete %s using hungryserv yeet endpoint: %v", portal.MXID, err)
		}
		return
	}

	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}
		return
	}

	portal.bridge.cleanupRoom(intent, portal.MXID, puppetsOnly, portal.log)
}

func (br *DiscordBridge) cleanupRoom(intent *appservice.IntentAPI, mxid id.RoomID, puppetsOnly bool, log log.Logger) {
	members, err := intent.JoinedMembers(mxid)
	if err != nil {
		log.Errorln("Failed to get portal members for cleanup:", err)
		return
	}

	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}

		puppet := br.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(mxid)
			if err != nil {
				log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(mxid, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}

	_, err = intent.LeaveRoom(mxid)
	if err != nil {
		log.Errorln("Error leaving with main intent while cleaning up portal:", err)
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

func (portal *Portal) handleMatrixReaction(sender *User, evt *event.Event) {
	if portal.IsPrivateChat() && sender.DiscordID != portal.Key.Receiver {
		go portal.sendMessageMetrics(evt, errUserNotReceiver, "Ignoring")
		return
	} else if !sender.IsLoggedIn() {
		//go portal.sendMessageMetrics(evt, errReactionUserNotLoggedIn, "Ignoring")
		return
	}

	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errUnknownRelationType, reaction.RelatesTo.Type), "Ignoring")
		return
	}

	if reaction.RelatesTo.Key == JoinThreadReaction {
		thread := portal.bridge.GetThreadByRootOrCreationNoticeMXID(reaction.RelatesTo.EventID)
		if thread == nil {
			go portal.sendMessageMetrics(evt, errTargetNotFound, "Ignoring thread join")
			return
		}
		thread.Join(sender)
		return
	}

	msg := portal.bridge.DB.Message.GetByMXID(portal.Key, reaction.RelatesTo.EventID)
	if msg == nil {
		go portal.sendMessageMetrics(evt, errTargetNotFound, "Ignoring")
		return
	}

	firstMsg := msg
	if msg.AttachmentID != "" {
		firstMsg = portal.bridge.DB.Message.GetFirstByDiscordID(portal.Key, msg.DiscordID)
		// TODO should the emoji be rerouted to the first message if it's different?
	}

	// Figure out if this is a custom emoji or not.
	emojiID := reaction.RelatesTo.Key
	if strings.HasPrefix(emojiID, "mxc://") {
		uri, _ := id.ParseContentURI(emojiID)
		emojiFile := portal.bridge.DB.File.GetByMXC(uri)
		if emojiFile == nil || emojiFile.ID == "" || emojiFile.EmojiName == "" {
			go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errUnknownEmoji, emojiID), "Ignoring")
			return
		}

		emojiID = fmt.Sprintf("%s:%s", emojiFile.EmojiName, emojiFile.ID)
	} else {
		emojiID = variationselector.FullyQualify(emojiID)
	}

	existing := portal.bridge.DB.Reaction.GetByDiscordID(portal.Key, msg.DiscordID, sender.DiscordID, emojiID)
	if existing != nil {
		portal.log.Debugfln("Dropping duplicate Matrix reaction %s (already sent as %s)", evt.ID, existing.MXID)
		go portal.sendMessageMetrics(evt, nil, "")
		return
	}

	err := sender.Session.MessageReactionAdd(msg.DiscordProtoChannelID(), msg.DiscordID, emojiID)
	go portal.sendMessageMetrics(evt, err, "Error sending")
	if err == nil {
		dbReaction := portal.bridge.DB.Reaction.New()
		dbReaction.Channel = portal.Key
		dbReaction.MessageID = msg.DiscordID
		dbReaction.FirstAttachmentID = firstMsg.AttachmentID
		dbReaction.Sender = sender.DiscordID
		dbReaction.EmojiName = emojiID
		dbReaction.ThreadID = msg.ThreadID
		dbReaction.MXID = evt.ID
		dbReaction.Insert()
	}
}

func (portal *Portal) handleDiscordReaction(user *User, reaction *discordgo.MessageReaction, add bool, thread *Thread, member *discordgo.Member) {
	puppet := portal.bridge.GetPuppetByID(reaction.UserID)
	if member != nil {
		puppet.UpdateInfo(user, member.User)
	}
	intent := puppet.IntentFor(portal)

	var discordID string
	var matrixReaction string

	if reaction.Emoji.ID != "" {
		reactionMXC := portal.getEmojiMXCByDiscordID(reaction.Emoji.ID, reaction.Emoji.Name, reaction.Emoji.Animated)
		if reactionMXC.IsEmpty() {
			return
		}
		matrixReaction = reactionMXC.String()
		discordID = fmt.Sprintf("%s:%s", reaction.Emoji.Name, reaction.Emoji.ID)
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
	existing := portal.bridge.DB.Reaction.GetByDiscordID(portal.Key, message[0].DiscordID, reaction.UserID, discordID)
	if !add {
		if existing == nil {
			portal.log.Debugln("Failed to remove reaction for unknown message", reaction.MessageID)
			return
		}

		resp, err := intent.RedactEvent(portal.MXID, existing.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to remove reaction from %s: %v", portal.MXID, err)
		} else {
			go portal.sendDeliveryReceipt(resp.EventID)
		}

		existing.Delete()
		return
	} else if existing != nil {
		portal.log.Debugfln("Ignoring duplicate reaction %s from %s to %s", discordID, reaction.UserID, message[0].DiscordID)
		return
	}

	content := event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			EventID: message[0].MXID,
			Type:    event.RelAnnotation,
			Key:     matrixReaction,
		},
	}
	extraContent := map[string]any{}
	if reaction.Emoji.ID != "" {
		extraContent["fi.mau.discord.reaction"] = map[string]any{
			"id":   reaction.Emoji.ID,
			"name": reaction.Emoji.Name,
			"mxc":  matrixReaction,
		}
		if !portal.bridge.Config.Bridge.CustomEmojiReactions {
			content.RelatesTo.Key = fmt.Sprintf(":%s:", reaction.Emoji.Name)
		}
	}

	resp, err := intent.SendMessageEvent(portal.MXID, event.EventReaction, &event.Content{
		Parsed: &content,
		Raw:    extraContent,
	})
	if err != nil {
		portal.log.Errorfln("failed to send reaction from %s: %v", reaction.MessageID, err)
		return
	}

	if existing == nil {
		dbReaction := portal.bridge.DB.Reaction.New()
		dbReaction.Channel = portal.Key
		dbReaction.MessageID = message[0].DiscordID
		dbReaction.FirstAttachmentID = message[0].AttachmentID
		dbReaction.Sender = reaction.UserID
		dbReaction.EmojiName = discordID
		dbReaction.MXID = resp.EventID
		if thread != nil {
			dbReaction.ThreadID = thread.ID
		}
		dbReaction.Insert()
		portal.sendDeliveryReceipt(dbReaction.MXID)
	}
}

func (portal *Portal) handleMatrixRedaction(sender *User, evt *event.Event) {
	if portal.IsPrivateChat() && sender.DiscordID != portal.Key.Receiver {
		go portal.sendMessageMetrics(evt, errUserNotReceiver, "Ignoring")
		return
	}

	sess := sender.Session
	if sess == nil && portal.RelayWebhookID == "" {
		go portal.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring")
		return
	}

	message := portal.bridge.DB.Message.GetByMXID(portal.Key, evt.Redacts)
	if message != nil {
		var err error
		// TODO add support for deleting individual attachments from messages
		if sess != nil {
			err = sess.ChannelMessageDelete(message.DiscordProtoChannelID(), message.DiscordID)
		} else {
			// TODO pre-validate that the message was sent by the webhook?
			err = relayClient.WebhookMessageDelete(portal.RelayWebhookID, portal.RelayWebhookSecret, message.DiscordID)
		}
		go portal.sendMessageMetrics(evt, err, "Error sending")
		if err == nil {
			message.Delete()
		}
		return
	}

	if sess != nil {
		reaction := portal.bridge.DB.Reaction.GetByMXID(evt.Redacts)
		if reaction != nil && reaction.Channel == portal.Key {
			err := sess.MessageReactionRemove(reaction.DiscordProtoChannelID(), reaction.MessageID, reaction.EmojiName, reaction.Sender)
			go portal.sendMessageMetrics(evt, err, "Error sending")
			if err == nil {
				reaction.Delete()
			}
			return
		}
	}

	go portal.sendMessageMetrics(evt, errTargetNotFound, "Ignoring")
}

func (portal *Portal) HandleMatrixReadReceipt(brUser bridge.User, eventID id.EventID, receipt event.ReadReceipt) {
	sender := brUser.(*User)
	if sender.Session == nil {
		return
	}
	var thread *Thread
	discordThreadID := ""
	if receipt.ThreadID != "" && receipt.ThreadID != event.ReadReceiptThreadMain {
		thread = portal.bridge.GetThreadByRootMXID(receipt.ThreadID)
		if thread != nil {
			discordThreadID = thread.ID
		}
	}
	if thread != nil {
		if portal.bridge.Config.Bridge.AutojoinThreadOnOpen {
			thread.Join(sender)
		}
		if eventID == thread.CreationNoticeMXID {
			portal.log.Debugfln("Dropping Matrix read receipt from %s for thread creation notice %s of %s", sender.MXID, thread.CreationNoticeMXID, thread.ID)
			return
		}
	}
	if !sender.Session.IsUser {
		// Drop read receipts from bot users (after checking for the thread auto-join stuff)
		return
	}
	msg := portal.bridge.DB.Message.GetByMXID(portal.Key, eventID)
	if msg == nil {
		msg = portal.bridge.DB.Message.GetClosestBefore(portal.Key, discordThreadID, receipt.Timestamp)
		if msg == nil {
			portal.log.Debugfln("Dropping Matrix read receipt from %s for %s: no messages found", sender.MXID, eventID)
			return
		} else {
			portal.log.Debugfln("Matrix read receipt target %s from %s not found, using closest message %s", eventID, sender.MXID, msg.MXID)
		}
	}
	if receipt.ThreadID != "" && msg.ThreadID != discordThreadID {
		portal.log.Debugfln("Dropping Matrix read receipt from %s for %s in unexpected thread (receipt: %s, message: %s)", receipt.ThreadID, msg.ThreadID)
		return
	}
	resp, err := sender.Session.ChannelMessageAckNoToken(msg.DiscordProtoChannelID(), msg.DiscordID)
	if err != nil {
		portal.log.Warnfln("Failed to handle read receipt for %s/%s from %s: %v", msg.MXID, msg.DiscordID, sender.MXID, err)
	} else if resp.Token != nil {
		portal.log.Debugfln("Marked %s/%s as read by %s (and got unexpected non-nil token %s)", msg.MXID, msg.DiscordID, sender.MXID, *resp.Token)
	} else {
		portal.log.Debugfln("Marked %s/%s in %s as read by %s", msg.MXID, msg.DiscordID, msg.DiscordProtoChannelID(), sender.MXID)
	}
}

func typingDiff(prev, new []id.UserID) (started []id.UserID) {
OuterNew:
	for _, userID := range new {
		for _, previousUserID := range prev {
			if userID == previousUserID {
				continue OuterNew
			}
		}
		started = append(started, userID)
	}
	return
}

func (portal *Portal) HandleMatrixTyping(newTyping []id.UserID) {
	portal.currentlyTypingLock.Lock()
	defer portal.currentlyTypingLock.Unlock()
	startedTyping := typingDiff(portal.currentlyTyping, newTyping)
	portal.currentlyTyping = newTyping
	for _, userID := range startedTyping {
		user := portal.bridge.GetUserByMXID(userID)
		if user != nil && user.Session != nil {
			user.ViewingChannel(portal)
			err := user.Session.ChannelTyping(portal.Key.ChannelID)
			if err != nil {
				portal.log.Warnfln("Failed to mark %s as typing: %v", user.MXID, err)
			} else {
				portal.log.Debugfln("Marked %s as typing", user.MXID)
			}
		}
	}
}

func (portal *Portal) UpdateName(meta *discordgo.Channel) bool {
	var parentName, guildName string
	if portal.Parent != nil {
		parentName = portal.Parent.PlainName
	}
	if portal.Guild != nil {
		guildName = portal.Guild.PlainName
	}
	plainNameChanged := portal.PlainName != meta.Name
	portal.PlainName = meta.Name
	return portal.UpdateNameDirect(portal.bridge.Config.Bridge.FormatChannelName(config.ChannelNameParams{
		Name:       meta.Name,
		ParentName: parentName,
		GuildName:  guildName,
		NSFW:       meta.NSFW,
		Type:       meta.Type,
	})) || plainNameChanged
}

func (portal *Portal) UpdateNameDirect(name string) bool {
	if portal.Name == name && (portal.NameSet || portal.MXID == "" || !portal.shouldSetDMRoomMetadata()) {
		return false
	}
	portal.log.Debugfln("Updating name %q -> %q", portal.Name, name)
	portal.Name = name
	portal.NameSet = false
	portal.updateRoomName()
	return true
}

func (portal *Portal) updateRoomName() {
	if portal.MXID != "" && portal.shouldSetDMRoomMetadata() {
		_, err := portal.MainIntent().SetRoomName(portal.MXID, portal.Name)
		if err != nil {
			portal.log.Warnln("Failed to update room name:", err)
		} else {
			portal.NameSet = true
		}
	}
}

func (portal *Portal) UpdateAvatarFromPuppet(puppet *Puppet) bool {
	if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && (portal.AvatarSet || portal.MXID == "" || !portal.shouldSetDMRoomMetadata()) {
		return false
	}
	portal.log.Debugfln("Updating avatar from puppet %q -> %q", portal.Avatar, puppet.Avatar)
	portal.Avatar = puppet.Avatar
	portal.AvatarURL = puppet.AvatarURL
	portal.AvatarSet = false
	portal.updateRoomAvatar()
	return true
}

func (portal *Portal) UpdateGroupDMAvatar(iconID string) bool {
	if portal.Avatar == iconID && (iconID == "") == portal.AvatarURL.IsEmpty() && (portal.AvatarSet || portal.MXID == "") {
		return false
	}
	portal.log.Debugfln("Updating group DM avatar %q -> %q", portal.Avatar, iconID)
	portal.Avatar = iconID
	portal.AvatarSet = false
	portal.AvatarURL = id.ContentURI{}
	if portal.Avatar != "" {
		uri, err := uploadAvatar(portal.MainIntent(), discordgo.EndpointGroupIcon(portal.Key.ChannelID, portal.Avatar))
		if err != nil {
			portal.log.Warnfln("Failed to reupload channel avatar %s: %v", portal.Avatar, err)
			return true
		} else {
			portal.AvatarURL = uri
		}
	}
	portal.updateRoomAvatar()
	return true
}

func (portal *Portal) updateRoomAvatar() {
	if portal.MXID == "" || portal.AvatarURL.IsEmpty() || !portal.shouldSetDMRoomMetadata() {
		return
	}
	_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
	if err != nil {
		portal.log.Warnln("Failed to update room avatar:", err)
	} else {
		portal.AvatarSet = true
	}
}

func (portal *Portal) UpdateTopic(topic string) bool {
	if portal.Topic == topic && (portal.TopicSet || portal.MXID == "") {
		return false
	}
	portal.log.Debugfln("Updating topic %q -> %q", portal.Topic, topic)
	portal.Topic = topic
	portal.TopicSet = false
	portal.updateRoomTopic()
	return true
}

func (portal *Portal) updateRoomTopic() {
	if portal.MXID != "" {
		_, err := portal.MainIntent().SetRoomTopic(portal.MXID, portal.Topic)
		if err != nil {
			portal.log.Warnln("Failed to update room topic:", err)
		} else {
			portal.TopicSet = true
		}
	}
}

func (portal *Portal) removeFromSpace() {
	if portal.InSpace == "" {
		return
	}

	portal.log.Debugfln("Removing room from space %s", portal.InSpace)
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, portal.InSpace.String(), struct{}{})
	if err != nil {
		portal.log.Warnfln("Failed to unset canonical space %s: %v", portal.InSpace, err)
	}
	_, err = portal.bridge.Bot.SendStateEvent(portal.InSpace, event.StateSpaceChild, portal.MXID.String(), struct{}{})
	if err != nil {
		portal.log.Warnfln("Failed to add room to space %s: %v", portal.InSpace, err)
	}
	portal.InSpace = ""
}

func (portal *Portal) addToSpace(mxid id.RoomID) bool {
	if portal.InSpace == mxid {
		return false
	}
	portal.removeFromSpace()
	if mxid == "" {
		return true
	}

	portal.log.Debugfln("Adding room to space %s", mxid)
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, mxid.String(), &event.SpaceParentEventContent{
		Via:       []string{portal.bridge.AS.HomeserverDomain},
		Canonical: true,
	})
	if err != nil {
		portal.log.Warnfln("Failed to set canonical space %s: %v", mxid, err)
	}

	_, err = portal.bridge.Bot.SendStateEvent(mxid, event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{portal.bridge.AS.HomeserverDomain},
		// TODO order
	})
	if err != nil {
		portal.log.Warnfln("Failed to add room to space %s: %v", mxid, err)
	} else {
		portal.InSpace = mxid
	}
	return true
}

func (portal *Portal) UpdateParent(parentID string) bool {
	if portal.ParentID == parentID {
		return false
	}
	portal.log.Debugfln("Updating parent ID %q -> %q", portal.ParentID, parentID)
	portal.ParentID = parentID
	if portal.ParentID != "" {
		portal.Parent = portal.bridge.GetPortalByID(database.NewPortalKey(parentID, ""), discordgo.ChannelTypeGuildCategory)
	} else {
		portal.Parent = nil
	}
	return true
}

func (portal *Portal) ExpectedSpaceID() id.RoomID {
	if portal.Parent != nil {
		return portal.Parent.MXID
	} else if portal.Guild != nil {
		return portal.Guild.MXID
	}
	return ""
}

func (portal *Portal) updateSpace() bool {
	if portal.MXID == "" {
		return false
	}
	if portal.Parent != nil {
		return portal.addToSpace(portal.Parent.MXID)
	} else if portal.Guild != nil {
		return portal.addToSpace(portal.Guild.MXID)
	}
	return false
}

func (portal *Portal) UpdateInfo(source *User, meta *discordgo.Channel) *discordgo.Channel {
	changed := false

	if meta == nil {
		portal.log.Debugfln("UpdateInfo called without metadata, fetching from %s's state cache", source.DiscordID)
		meta, _ = source.Session.State.Channel(portal.Key.ChannelID)
		if meta == nil {
			portal.log.Warnfln("No metadata found in state cache, fetching from server via %s", source.DiscordID)
			var err error
			meta, err = source.Session.Channel(portal.Key.ChannelID)
			if err != nil {
				portal.log.Errorfln("Failed to fetch meta via %s: %v", source.DiscordID, err)
				return nil
			}
		}
	}

	if portal.Type != meta.Type {
		portal.log.Warnfln("Portal type changed from %d to %d", portal.Type, meta.Type)
		portal.Type = meta.Type
		changed = true
	}
	if portal.OtherUserID == "" && portal.IsPrivateChat() {
		if len(meta.Recipients) == 0 {
			var err error
			meta, err = source.Session.Channel(meta.ID)
			if err != nil {
				portal.log.Errorfln("Failed to get DM channel info:", err)
			}
		}
		portal.OtherUserID = meta.Recipients[0].ID
		portal.log.Infoln("Found other user ID:", portal.OtherUserID)
		changed = true
	}
	if meta.GuildID != "" && portal.GuildID == "" {
		portal.GuildID = meta.GuildID
		portal.Guild = portal.bridge.GetGuildByID(portal.GuildID, true)
		changed = true
	}

	switch portal.Type {
	case discordgo.ChannelTypeDM:
		if portal.OtherUserID != "" {
			puppet := portal.bridge.GetPuppetByID(portal.OtherUserID)
			changed = portal.UpdateAvatarFromPuppet(puppet) || changed
			changed = portal.UpdateNameDirect(puppet.Name) || changed
		}
	case discordgo.ChannelTypeGroupDM:
		changed = portal.UpdateGroupDMAvatar(meta.Icon) || changed
		fallthrough
	default:
		changed = portal.UpdateName(meta) || changed
	}
	changed = portal.UpdateTopic(meta.Topic) || changed
	changed = portal.UpdateParent(meta.ParentID) || changed
	// Private channels are added to the space in User.handlePrivateChannel
	if portal.GuildID != "" && portal.MXID != "" && portal.ExpectedSpaceID() != portal.InSpace {
		changed = portal.updateSpace() || changed
	}
	if changed {
		portal.UpdateBridgeInfo()
		portal.Update()
	}
	return meta
}
