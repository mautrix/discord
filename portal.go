package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gabriel-vasile/mimetype"
	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exsync"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

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
	log    zerolog.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex

	discordMessages chan portalDiscordMessage
	matrixMessages  chan portalMatrixMessage

	recentMessages *exsync.RingBuffer[string, *discordgo.Message]

	commands     map[string]*discordgo.ApplicationCommand
	commandsLock sync.RWMutex

	forwardBackfillLock sync.Mutex

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

func (user *User) FindPrivateChatWith(userID string) *Portal {
	user.bridge.portalsLock.Lock()
	defer user.bridge.portalsLock.Unlock()
	dbPortal := user.bridge.DB.Portal.FindPrivateChatBetween(userID, user.DiscordID)
	if dbPortal == nil {
		return nil
	}
	existing, ok := user.bridge.portalsByID[dbPortal.Key]
	if ok {
		return existing
	}
	return user.bridge.loadPortal(dbPortal, nil, discordgo.ChannelTypeDM)
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
		log: br.ZLog.With().
			Str("channel_id", dbPortal.Key.ChannelID).
			Str("channel_receiver", dbPortal.Key.Receiver).
			Str("room_id", dbPortal.MXID.String()).
			Logger(),

		discordMessages: make(chan portalDiscordMessage, br.Config.Bridge.PortalMessageBuffer),
		matrixMessages:  make(chan portalMatrixMessage, br.Config.Bridge.PortalMessageBuffer),

		recentMessages: exsync.NewRingBuffer[string, *discordgo.Message](recentMessageBufferSize),

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
	RoomType   string `json:"com.beeper.room_type,omitempty"`
	RoomTypeV2 string `json:"com.beeper.room_type.v2,omitempty"`
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
	var roomTypeV2 string
	if portal.Type == discordgo.ChannelTypeDM {
		roomTypeV2 = "dm"
	} else if portal.Type == discordgo.ChannelTypeGroupDM {
		roomTypeV2 = "group_dm"
	}

	return bridgeInfoStateKey, CustomBridgeInfoContent{bridgeInfo, roomType, roomTypeV2}
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debug().Msg("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debug().Msg("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warn().Err(err).Msg("Failed to update m.bridge")
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		portal.log.Warn().Err(err).Msg("Failed to update uk.half-shot.bridge")
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
		portal.ensureUserInvited(user, false)
		return nil
	}
	portal.log.Info().Msg("Creating Matrix room for channel")

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
					RoomID: portal.Guild.MXID,
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
	if !portal.shouldSetDMRoomMetadata() && !portal.FriendNick {
		req.Name = ""
	}

	var backfillStarted bool
	portal.forwardBackfillLock.Lock()
	defer func() {
		if !backfillStarted {
			portal.log.Debug().Msg("Backfill wasn't started, unlocking forward backfill lock")
			portal.forwardBackfillLock.Unlock()
		}
	}()

	resp, err := intent.CreateRoom(req)
	if err != nil {
		portal.log.Warn().Err(err).Msg("Failed to create room")
		return err
	}

	portal.NameSet = len(req.Name) > 0
	portal.TopicSet = len(req.Topic) > 0
	portal.MXID = resp.RoomID
	portal.log = portal.bridge.ZLog.With().
		Str("channel_id", portal.Key.ChannelID).
		Str("channel_receiver", portal.Key.Receiver).
		Str("room_id", portal.MXID.String()).
		Logger()
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	portal.Update()
	portal.log.Info().Msg("Matrix room created")

	if portal.Encrypted && portal.IsPrivateChat() {
		err = portal.bridge.Bot.EnsureJoined(portal.MXID, appservice.EnsureJoinedParams{BotOverride: portal.MainIntent().Client})
		if err != nil {
			portal.log.Err(err).Msg("Failed to ensure bridge bot is joined to encrypted private chat portal")
		}
	}

	if portal.GuildID == "" {
		user.addPrivateChannelToSpace(portal)
	} else {
		portal.updateSpace(user)
	}
	portal.ensureUserInvited(user, true)
	user.syncChatDoublePuppetDetails(portal, true)

	portal.syncParticipants(user, channel.Recipients)

	if portal.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByID(portal.Key.Receiver)

		chats := map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}}
		user.updateDirectChats(chats)
	}

	firstEventResp, err := portal.MainIntent().SendMessageEvent(portal.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		portal.log.Err(err).Msg("Failed to send dummy event to mark portal creation")
	} else {
		portal.FirstEventID = firstEventResp.EventID
		portal.Update()
	}

	go portal.forwardBackfillInitial(user, nil)
	backfillStarted = true

	return nil
}

func (portal *Portal) handleDiscordMessages(msg portalDiscordMessage) {
	if portal.MXID == "" {
		msgCreate, ok := msg.msg.(*discordgo.MessageCreate)
		if !ok {
			portal.log.Warn().Msg("Can't create Matrix room from non new message event")
			return
		}

		portal.log.Debug().
			Str("message_id", msgCreate.ID).
			Msg("Creating Matrix room from incoming message")
		if err := portal.CreateMatrixRoom(msg.user, nil); err != nil {
			portal.log.Err(err).Msg("Failed to create portal room")
			return
		}
	}
	portal.forwardBackfillLock.Lock()
	defer portal.forwardBackfillLock.Unlock()

	switch convertedMsg := msg.msg.(type) {
	case *discordgo.MessageCreate:
		portal.handleDiscordMessageCreate(msg.user, convertedMsg.Message, msg.thread)
	case *discordgo.MessageUpdate:
		portal.handleDiscordMessageUpdate(msg.user, convertedMsg.Message)
	case *discordgo.MessageDelete:
		portal.handleDiscordMessageDelete(msg.user, convertedMsg.Message)
	case *discordgo.MessageDeleteBulk:
		portal.handleDiscordMessageDeleteBulk(msg.user, convertedMsg.Messages)
	case *discordgo.MessageReactionAdd:
		portal.handleDiscordReaction(msg.user, convertedMsg.MessageReaction, true, msg.thread, convertedMsg.Member)
	case *discordgo.MessageReactionRemove:
		portal.handleDiscordReaction(msg.user, convertedMsg.MessageReaction, false, msg.thread, nil)
	default:
		portal.log.Warn().Type("message_type", msg.msg).Msg("Unknown message type in handleDiscordMessages")
	}
}

func (portal *Portal) ensureUserInvited(user *User, ignoreCache bool) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat(), ignoreCache)
}

func (portal *Portal) markMessageHandled(discordID string, authorID string, timestamp time.Time, threadID string, senderMXID id.UserID, parts []database.MessagePart) *database.Message {
	msg := portal.bridge.DB.Message.New()
	msg.Channel = portal.Key
	msg.DiscordID = discordID
	msg.SenderID = authorID
	msg.Timestamp = timestamp
	msg.ThreadID = threadID
	msg.SenderMXID = senderMXID
	msg.MassInsertParts(parts)
	msg.MXID = parts[0].MXID
	msg.AttachmentID = parts[0].AttachmentID
	return msg
}

func (portal *Portal) handleDiscordMessageCreate(user *User, msg *discordgo.Message, thread *Thread) {
	switch msg.Type {
	case discordgo.MessageTypeChannelNameChange, discordgo.MessageTypeChannelIconChange, discordgo.MessageTypeChannelPinnedMessage:
		// These are handled via channel updates
		return
	}

	log := portal.log.With().
		Str("message_id", msg.ID).
		Int("message_type", int(msg.Type)).
		Str("author_id", msg.Author.ID).
		Str("action", "discord message create").
		Logger()
	ctx := log.WithContext(context.Background())

	portal.recentMessages.Push(msg.ID, msg)

	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing != nil {
		log.Debug().Msg("Dropping duplicate message")
		return
	}

	handlingStartTime := time.Now()
	puppet := portal.bridge.GetPuppetByID(msg.Author.ID)
	puppet.UpdateInfo(user, msg.Author, msg)
	intent := puppet.IntentFor(portal)

	var discordThreadID string
	var threadRootEvent, lastThreadEvent id.EventID
	if thread != nil {
		discordThreadID = thread.ID
		threadRootEvent = thread.RootMXID
		lastThreadEvent = threadRootEvent
		lastInThread := portal.bridge.DB.Message.GetLastInThread(portal.Key, thread.ID)
		if lastInThread != nil {
			lastThreadEvent = lastInThread.MXID
		}
	}
	replyTo := portal.getReplyTarget(user, discordThreadID, msg.MessageReference, msg.Embeds, false)
	mentions := portal.convertDiscordMentions(msg, true)

	ts, _ := discordgo.SnowflakeTimestamp(msg.ID)

	msgBody := msg

	if msg.MessageReference != nil && msg.MessageReference.Type == discordgo.MessageReferenceTypeForward {
		if len(msg.MessageSnapshots) == 0 {
			log.Debug().Msg("No message snapshot available for forwarded message, skipping")
			return
		}

		msgBody = msg.MessageSnapshots[0].Message

		var content = msgBody.Content
		msgBody.Content = "↷ Forwarded"

		if content != "" {
			msgBody.Content += "\n" + content
		}
	}

	parts := portal.convertDiscordMessage(ctx, puppet, intent, msgBody)
	dbParts := make([]database.MessagePart, 0, len(parts))
	eventIDs := zerolog.Dict()
	for i, part := range parts {
		if (replyTo != nil || threadRootEvent != "") && part.Content.RelatesTo == nil {
			part.Content.RelatesTo = &event.RelatesTo{}
		}
		if threadRootEvent != "" {
			part.Content.RelatesTo.SetThread(threadRootEvent, lastThreadEvent)
		}
		if replyTo != nil {
			part.Content.RelatesTo.SetReplyTo(replyTo.EventID)
			if replyTo.UnstableRoomID != "" {
				part.Content.RelatesTo.InReplyTo.UnstableRoomID = replyTo.UnstableRoomID
			}
			// Only set reply for first event
			replyTo = nil
		}

		part.Content.Mentions = mentions
		// Only set mentions for first event, but keep empty object for rest
		mentions = &event.Mentions{}

		resp, err := portal.sendMatrixMessage(intent, part.Type, part.Content, part.Extra, ts.UnixMilli())
		if err != nil {
			log.Err(err).
				Int("part_index", i).
				Str("attachment_id", part.AttachmentID).
				Msg("Failed to send part of message to Matrix")
			continue
		}
		lastThreadEvent = resp.EventID
		dbParts = append(dbParts, database.MessagePart{AttachmentID: part.AttachmentID, MXID: resp.EventID})
		eventIDs.Str(part.AttachmentID, resp.EventID.String())
	}

	log = log.With().Dur("handling_time", time.Since(handlingStartTime)).Logger()
	if len(parts) == 0 {
		log.Warn().Msg("Unhandled message")
	} else if len(dbParts) == 0 {
		log.Warn().Msg("All parts of message failed to send to Matrix")
	} else {
		log.Debug().Dict("event_ids", eventIDs).Msg("Finished handling Discord message")
		firstDBMessage := portal.markMessageHandled(msg.ID, msg.Author.ID, ts, discordThreadID, intent.UserID, dbParts)
		if msg.Flags == discordgo.MessageFlagsHasThread {
			portal.bridge.threadFound(ctx, user, firstDBMessage, msg.ID, msg.Thread)
		}
	}
}

var hackyReplyPattern = regexp.MustCompile(`^\*\*\[Replying to]\(https://discord.com/channels/(\d+)/(\d+)/(\d+)\)`)

func isReplyEmbed(embed *discordgo.MessageEmbed) bool {
	return hackyReplyPattern.MatchString(embed.Description)
}

func (portal *Portal) getReplyTarget(source *User, threadID string, ref *discordgo.MessageReference, embeds []*discordgo.MessageEmbed, allowNonExistent bool) *event.InReplyTo {
	if ref == nil && len(embeds) > 0 {
		match := hackyReplyPattern.FindStringSubmatch(embeds[0].Description)
		if match != nil && match[1] == portal.GuildID && (match[2] == portal.Key.ChannelID || match[2] == threadID) {
			ref = &discordgo.MessageReference{
				MessageID: match[3],
				ChannelID: match[2],
				GuildID:   match[1],
			}
		}
	}
	if ref == nil {
		return nil
	}
	// TODO add config option for cross-room replies
	crossRoomReplies := portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry

	targetPortal := portal
	if ref.ChannelID != portal.Key.ChannelID && ref.ChannelID != threadID && crossRoomReplies {
		targetPortal = portal.bridge.GetExistingPortalByID(database.PortalKey{ChannelID: ref.ChannelID, Receiver: source.DiscordID})
		if targetPortal == nil {
			return nil
		}
	}
	replyToMsg := portal.bridge.DB.Message.GetByDiscordID(targetPortal.Key, ref.MessageID)
	if len(replyToMsg) > 0 {
		if !crossRoomReplies {
			return &event.InReplyTo{EventID: replyToMsg[0].MXID}
		}
		return &event.InReplyTo{
			EventID:        replyToMsg[0].MXID,
			UnstableRoomID: targetPortal.MXID,
		}
	} else if allowNonExistent {
		return &event.InReplyTo{
			EventID:        targetPortal.deterministicEventID(ref.MessageID, ""),
			UnstableRoomID: targetPortal.MXID,
		}
	}
	return nil
}

const JoinThreadReaction = "join thread"

func (portal *Portal) sendThreadCreationNotice(ctx context.Context, thread *Thread) {
	thread.creationNoticeLock.Lock()
	defer thread.creationNoticeLock.Unlock()
	if thread.CreationNoticeMXID != "" {
		return
	}
	creationNotice := "Thread created. React to this message with \"join thread\" to join the thread on Discord."
	if portal.bridge.Config.Bridge.AutojoinThreadOnOpen {
		creationNotice = "Thread created. Opening this thread will auto-join you to it on Discord."
	}
	log := zerolog.Ctx(ctx)
	resp, err := portal.sendMatrixMessage(portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
		Body:      creationNotice,
		MsgType:   event.MsgNotice,
		RelatesTo: (&event.RelatesTo{}).SetThread(thread.RootMXID, thread.RootMXID),
	}, nil, time.Now().UnixMilli())
	if err != nil {
		log.Err(err).Msg("Failed to send thread creation notice")
		return
	}
	portal.bridge.threadsLock.Lock()
	thread.CreationNoticeMXID = resp.EventID
	portal.bridge.threadsByCreationNoticeMXID[resp.EventID] = thread
	portal.bridge.threadsLock.Unlock()
	thread.Update()
	log.Debug().
		Str("creation_notice_mxid", thread.CreationNoticeMXID.String()).
		Msg("Sent thread creation notice")

	resp, err = portal.MainIntent().SendMessageEvent(portal.MXID, event.EventReaction, &event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: thread.CreationNoticeMXID,
			Key:     JoinThreadReaction,
		},
	})
	if err != nil {
		log.Err(err).Msg("Failed to send prefilled reaction to thread creation notice")
	} else {
		log.Debug().
			Str("reaction_event_id", resp.EventID.String()).
			Str("creation_notice_mxid", thread.CreationNoticeMXID.String()).
			Msg("Sent prefilled reaction to thread creation notice")
	}
}

func (portal *Portal) handleDiscordMessageUpdate(user *User, msg *discordgo.Message) {
	log := portal.log.With().
		Str("message_id", msg.ID).
		Str("action", "discord message update").
		Logger()
	ctx := log.WithContext(context.Background())
	if portal.MXID == "" {
		log.Warn().Msg("handle message called without a valid portal")
		return
	}

	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msg.ID)
	if existing == nil {
		log.Warn().Msg("Dropping update of unknown message")
		return
	}
	if msg.EditedTimestamp != nil && !msg.EditedTimestamp.After(existing[0].EditTimestamp) {
		log.Debug().
			Time("received_edit_ts", *msg.EditedTimestamp).
			Time("db_edit_ts", existing[0].EditTimestamp).
			Msg("Dropping update of message with older or equal edit timestamp")
		return
	}

	if msg.Flags == discordgo.MessageFlagsHasThread {
		portal.bridge.threadFound(ctx, user, existing[0], msg.ID, msg.Thread)
	}

	if msg.Author == nil {
		creationMessage, ok := portal.recentMessages.Get(msg.ID)
		if !ok {
			log.Debug().Msg("Dropping edit with no author of non-recent message")
			return
		} else if creationMessage.Type == discordgo.MessageTypeCall {
			log.Debug().Msg("Dropping edit with of call message")
			return
		}
		log.Debug().Msg("Found original message in cache for edit without author")
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
	if msg.Author.ID == portal.RelayWebhookID {
		log.Debug().
			Str("message_id", msg.ID).
			Str("author_id", msg.Author.ID).
			Msg("Dropping edit from relay webhook")
		return
	}

	puppet := portal.bridge.GetPuppetByID(msg.Author.ID)
	intent := puppet.IntentFor(portal)

	redactions := zerolog.Dict()
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
		if getEmbedType(nil, remainingEmbed) != EmbedVideo {
			continue
		}
		embedID := "video_" + remainingEmbed.URL
		if _, found := attachmentMap[embedID]; found {
			delete(attachmentMap, embedID)
		}
	}
	for _, deletedAttachment := range attachmentMap {
		resp, err := intent.RedactEvent(portal.MXID, deletedAttachment.MXID)
		if err != nil {
			log.Err(err).
				Str("event_id", deletedAttachment.MXID.String()).
				Msg("Failed to redact attachment")
		} else {
			redactions.Str(deletedAttachment.AttachmentID, resp.EventID.String())
		}
		deletedAttachment.Delete()
	}

	var converted *ConvertedMessage
	// Slightly hacky special case: messages with gif links will get an embed with the gif.
	// The link isn't rendered on Discord, so just edit the link message into a gif message on Matrix too.
	if isPlainGifMessage(msg) {
		converted = portal.convertDiscordVideoEmbed(ctx, intent, msg.Embeds[0])
	} else {
		converted = portal.convertDiscordTextMessage(ctx, intent, msg)
	}
	if converted == nil {
		log.Debug().
			Bool("has_message_on_matrix", existing[0].AttachmentID == "").
			Bool("has_text_on_discord", len(msg.Content) > 0).
			Msg("Dropping non-text edit")
		return
	}
	puppet.addWebhookMeta(converted, msg)
	puppet.addMemberMeta(converted, msg)
	converted.Content.Mentions = portal.convertDiscordMentions(msg, false)
	converted.Content.SetEdit(existing[0].MXID)
	// Never actually mention new users of edits, only include mentions inside m.new_content
	converted.Content.Mentions = &event.Mentions{}
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
		log.Err(err).Msg("Failed to send edit to Matrix")
		return
	}

	portal.sendDeliveryReceipt(resp.EventID)

	if msg.EditedTimestamp != nil {
		existing[0].UpdateEditTimestamp(*msg.EditedTimestamp)
	}
	log.Debug().
		Str("event_id", resp.EventID.String()).
		Dict("redacted_attachments", redactions).
		Msg("Finished handling Discord edit")
}

func (portal *Portal) handleDiscordMessageDelete(user *User, msg *discordgo.Message) {
	lastResp := portal.redactAllParts(portal.MainIntent(), msg.ID)
	if lastResp != "" {
		portal.sendDeliveryReceipt(lastResp)
	}
}

func (portal *Portal) handleDiscordMessageDeleteBulk(user *User, messages []string) {
	intent := portal.MainIntent()
	var lastResp id.EventID
	for _, msgID := range messages {
		newLastResp := portal.redactAllParts(intent, msgID)
		if newLastResp != "" {
			lastResp = newLastResp
		}
	}
	if lastResp != "" {
		portal.sendDeliveryReceipt(lastResp)
	}
}

func (portal *Portal) redactAllParts(intent *appservice.IntentAPI, msgID string) (lastResp id.EventID) {
	existing := portal.bridge.DB.Message.GetByDiscordID(portal.Key, msgID)
	for _, dbMsg := range existing {
		resp, err := intent.RedactEvent(portal.MXID, dbMsg.MXID)
		if err != nil {
			portal.log.Err(err).
				Str("message_id", msgID).
				Str("event_id", dbMsg.MXID.String()).
				Msg("Failed to redact Matrix message")
		} else if resp != nil && resp.EventID != "" {
			lastResp = resp.EventID
		}
		dbMsg.Delete()
	}
	return
}

func (portal *Portal) handleDiscordTyping(evt *discordgo.TypingStart) {
	puppet := portal.bridge.GetPuppetByID(evt.UserID)
	if puppet.Name == "" {
		// Puppet hasn't been synced yet
		return
	}
	log := portal.log.With().
		Str("ghost_mxid", puppet.MXID.String()).
		Str("action", "discord typing").
		Logger()
	intent := puppet.IntentFor(portal)
	err := intent.EnsureJoined(portal.MXID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to ensure ghost is joined for typing notification")
		return
	}
	_, err = intent.UserTyping(portal.MXID, true, 12*time.Second)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to send typing notification to Matrix")
	}
}

func (portal *Portal) syncParticipant(source *User, participant *discordgo.User, remove bool) {
	puppet := portal.bridge.GetPuppetByID(participant.ID)
	puppet.UpdateInfo(source, participant, nil)
	log := portal.log.With().
		Str("participant_id", participant.ID).
		Str("ghost_mxid", puppet.MXID.String()).
		Logger()

	user := portal.bridge.GetUserByID(participant.ID)
	if user != nil {
		log.Debug().Msg("Ensuring Matrix user is invited or joined to room")
		portal.ensureUserInvited(user, false)
	}

	if remove {
		_, err := puppet.DefaultIntent().LeaveRoom(portal.MXID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to make ghost leave room after member remove event")
		}
	} else if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
		if err := puppet.IntentFor(portal).EnsureJoined(portal.MXID); err != nil {
			log.Warn().Err(err).Msg("Failed to add ghost to room")
		}
	}
}

func (portal *Portal) syncParticipants(source *User, participants []*discordgo.User) {
	for _, participant := range participants {
		puppet := portal.bridge.GetPuppetByID(participant.ID)
		puppet.UpdateInfo(source, participant, nil)

		var user *User
		if participant.ID != portal.OtherUserID {
			user = portal.bridge.GetUserByID(participant.ID)
			if user != nil {
				portal.ensureUserInvited(user, false)
			}
		}

		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			if err := puppet.IntentFor(portal).EnsureJoined(portal.MXID); err != nil {
				portal.log.Warn().Err(err).
					Str("participant_id", participant.ID).
					Msg("Failed to add ghost to room")
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
	portal.forwardBackfillLock.Lock()
	defer portal.forwardBackfillLock.Unlock()
	switch msg.evt.Type {
	case event.EventMessage, event.EventSticker:
		portal.handleMatrixMessage(msg.user, msg.evt)
	case event.EventRedaction:
		portal.handleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(msg.user, msg.evt)
	default:
		portal.log.Warn().Str("event_type", msg.evt.Type.Type).Msg("Unknown event type in handleMatrixMessages")
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
			return nil, fmt.Errorf("failed to decrypt event: %w", err)
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
		}, portal.RefererOptIfUser(sender.Session, "")...)
		if err != nil {
			return "", fmt.Errorf("error starting thread: %v", err)
		}
		portal.log.Debug().
			Str("thread_root_mxid", threadRoot.String()).
			Str("thread_id", ch.ID).
			Msg("Created Discord thread")
		portal.bridge.GetThreadByID(existingMsg.DiscordID, existingMsg)
		return ch.ID, nil
	}
}

func (portal *Portal) sendErrorMessage(evt *event.Event, msgType, message string, confirmed bool) id.EventID {
	if !portal.bridge.Config.Bridge.MessageErrorNotices {
		return ""
	}
	certainty := "may not have been"
	if confirmed {
		certainty = "was not"
	}
	if portal.RelayWebhookSecret != "" {
		message = strings.ReplaceAll(message, portal.RelayWebhookSecret, "<redacted>")
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("\u26a0 Your %s %s bridged: %v", msgType, certainty, message),
	}
	relatable, ok := evt.Content.Parsed.(event.Relatable)
	if ok && relatable.OptionalGetRelatesTo().GetThreadParent() != "" {
		content.GetRelatesTo().SetThread(relatable.OptionalGetRelatesTo().GetThreadParent(), evt.ID)
	}
	resp, err := portal.sendMatrixMessage(portal.MainIntent(), event.EventMessage, content, nil, 0)
	if err != nil {
		portal.log.Warn().Err(err).Msg("Failed to send bridging error message")
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

func errorToStatusReason(err error) (reason event.MessageStatusReason, status event.MessageStatus, isCertain, sendNotice bool, humanMessage string, checkpointError error) {
	var restErr *discordgo.RESTError
	switch {
	case errors.Is(err, errUnknownMsgType),
		errors.Is(err, errUnknownRelationType),
		errors.Is(err, errUnexpectedParsedContentType),
		errors.Is(err, errUnknownEmoji),
		errors.Is(err, id.InvalidContentURI),
		errors.Is(err, attachment.UnsupportedVersion),
		errors.Is(err, attachment.UnsupportedAlgorithm),
		errors.Is(err, errCantStartThread):
		return event.MessageStatusUnsupported, event.MessageStatusFail, true, true, "", nil
	case errors.Is(err, attachment.HashMismatch),
		errors.Is(err, attachment.InvalidKey),
		errors.Is(err, attachment.InvalidInitVector):
		return event.MessageStatusUndecryptable, event.MessageStatusFail, true, true, "", nil
	case errors.Is(err, errUserNotReceiver), errors.Is(err, errUserNotLoggedIn):
		return event.MessageStatusNoPermission, event.MessageStatusFail, true, false, "", nil
	case errors.Is(err, errUnknownEditTarget):
		return event.MessageStatusGenericError, event.MessageStatusFail, true, false, "", nil
	case errors.Is(err, errTargetNotFound):
		return event.MessageStatusGenericError, event.MessageStatusFail, true, false, "", nil
	case errors.As(err, &restErr):
		if restErr.Message != nil && (restErr.Message.Code != 0 || len(restErr.Message.Message) > 0) {
			reason, humanMessage = restErrorToStatusReason(restErr.Message)
			status = event.MessageStatusFail
			isCertain = true
			sendNotice = true
			checkpointError = fmt.Errorf("HTTP %d: %d: %s", restErr.Response.StatusCode, restErr.Message.Code, restErr.Message.Message)
			if len(restErr.Message.Errors) > 0 {
				jsonExtraErrors, _ := json.Marshal(restErr.Message.Errors)
				checkpointError = fmt.Errorf("%w (%s)", checkpointError, jsonExtraErrors)
			}
			return
		} else if restErr.Response.StatusCode == http.StatusBadRequest && bytes.HasPrefix(restErr.ResponseBody, []byte(`{"captcha_key"`)) {
			return event.MessageStatusGenericError, event.MessageStatusRetriable, true, true, "Captcha error", errors.New("captcha required")
		} else if restErr.Response != nil && (restErr.Response.StatusCode == http.StatusServiceUnavailable || restErr.Response.StatusCode == http.StatusBadGateway || restErr.Response.StatusCode == http.StatusGatewayTimeout) {
			return event.MessageStatusGenericError, event.MessageStatusRetriable, true, true, fmt.Sprintf("HTTP %s", restErr.Response.Status), fmt.Errorf("HTTP %d", restErr.Response.StatusCode)
		}
		fallthrough
	case errors.Is(err, context.DeadlineExceeded):
		return event.MessageStatusTooOld, event.MessageStatusRetriable, false, true, "", context.DeadlineExceeded
	case strings.HasSuffix(err.Error(), "(Client.Timeout exceeded while awaiting headers)"):
		return event.MessageStatusTooOld, event.MessageStatusRetriable, false, true, "", errors.New("HTTP request timed out")
	case errors.Is(err, syscall.ECONNRESET):
		return event.MessageStatusGenericError, event.MessageStatusRetriable, false, true, "", errors.New("connection reset")
	default:
		return event.MessageStatusGenericError, event.MessageStatusRetriable, false, true, "", nil
	}
}

func restErrorToStatusReason(msg *discordgo.APIErrorMessage) (reason event.MessageStatusReason, humanMessage string) {
	switch msg.Code {
	case discordgo.ErrCodeRequestEntityTooLarge:
		return event.MessageStatusUnsupported, "Attachment is too large"
	case discordgo.ErrCodeUnknownEmoji:
		return event.MessageStatusUnsupported, "Unsupported emoji"
	case discordgo.ErrCodeMissingPermissions, discordgo.ErrCodeMissingAccess:
		return event.MessageStatusUnsupported, "You don't have the permissions to do that"
	case discordgo.ErrCodeCannotSendMessagesToThisUser:
		return event.MessageStatusUnsupported, "You can't send messages to this user"
	case discordgo.ErrCodeCannotSendMessagesInVoiceChannel:
		return event.MessageStatusUnsupported, "You can't send messages in a non-text channel"
	case discordgo.ErrCodeInvalidFormBody:
		contentErrs := msg.Errors["content"].Errors
		if len(contentErrs) == 1 && contentErrs[0].Code == "BASE_TYPE_MAX_LENGTH" {
			return event.MessageStatusUnsupported, "Message is too long: " + contentErrs[0].Message
		}
	}
	return event.MessageStatusGenericError, fmt.Sprintf("%d: %s", msg.Code, msg.Message)
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
		var checkpointErr error
		content.Reason, content.Status, _, _, content.Message, checkpointErr = errorToStatusReason(err)
		if checkpointErr != nil {
			content.Error = checkpointErr.Error()
		} else {
			content.Error = err.Error()
		}
	}
	_, err = intent.SendMessageEvent(portal.MXID, event.BeeperMessageStatus, &content)
	if err != nil {
		portal.log.Err(err).Str("event_id", evtID.String()).Msg("Failed to send message status event")
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
	level := zerolog.DebugLevel
	if err != nil && part != "Ignoring" {
		level = zerolog.ErrorLevel
	}
	logEvt := portal.log.WithLevel(level).
		Str("action", "send matrix message metrics").
		Str("event_type", evt.Type.Type).
		Str("event_id", evt.ID.String()).
		Str("sender", evt.Sender.String())
	if evt.Type == event.EventRedaction {
		logEvt.Str("redacts", evt.Redacts.String())
	}
	if err != nil {
		logEvt.Err(err).
			Str("result", fmt.Sprintf("%s event", part)).
			Msg("Matrix event not handled")
		reason, statusCode, isCertain, sendNotice, humanMessage, checkpointErr := errorToStatusReason(err)
		if checkpointErr == nil {
			checkpointErr = err
		}
		checkpointStatus := status.ReasonToCheckpointStatus(reason, statusCode)
		portal.bridge.SendMessageCheckpoint(evt, status.MsgStepRemote, checkpointErr, checkpointStatus, 0)
		if sendNotice {
			if humanMessage == "" {
				humanMessage = err.Error()
			}
			portal.sendErrorMessage(evt, msgType, humanMessage, isCertain)
		}
		portal.sendStatusEvent(evt.ID, err)
	} else {
		logEvt.Err(err).Msg("Matrix event handled successfully")
		portal.sendDeliveryReceipt(evt.ID)
		portal.bridge.SendMessageSuccessCheckpoint(evt, status.MsgStepRemote, 0)
		portal.sendStatusEvent(evt.ID, nil)
	}
}

func (br *DiscordBridge) serveMediaProxy(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	mxc := id.ContentURI{
		Homeserver: vars["server"],
		FileID:     vars["mediaID"],
	}
	checksum, err := base64.RawURLEncoding.DecodeString(vars["checksum"])
	if err != nil || len(checksum) != 32 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, expectedChecksum := br.hashMediaProxyURL(mxc)
	if !hmac.Equal(checksum, expectedChecksum) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	reader, err := br.Bot.Download(mxc)
	if err != nil {
		br.ZLog.Warn().Err(err).Msg("Failed to download media to proxy")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	buf := make([]byte, 32*1024)
	n, err := io.ReadFull(reader, buf)
	if err != nil && (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
		br.ZLog.Warn().Err(err).Msg("Failed to read first part of media to proxy")
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.Header().Add("Content-Type", http.DetectContentType(buf[:n]))
	if n < len(buf) {
		w.Header().Add("Content-Length", strconv.Itoa(n))
	}
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(buf[:n])
	if err != nil {
		return
	}
	if n >= len(buf) {
		_, _ = io.CopyBuffer(w, reader, buf)
	}
}

func (br *DiscordBridge) hashMediaProxyURL(mxc id.ContentURI) (string, []byte) {
	path := fmt.Sprintf("/mautrix-discord/avatar/%s/%s/", mxc.Homeserver, mxc.FileID)
	checksum := hmac.New(sha256.New, []byte(br.Config.Bridge.AvatarProxyKey))
	checksum.Write([]byte(path))
	return path, checksum.Sum(nil)
}

func (br *DiscordBridge) makeMediaProxyURL(mxc id.ContentURI) string {
	if br.Config.Bridge.PublicAddress == "" {
		return ""
	}
	path, checksum := br.hashMediaProxyURL(mxc)
	return br.Config.Bridge.PublicAddress + path + base64.RawURLEncoding.EncodeToString(checksum)
}

func (portal *Portal) getRelayUserMeta(sender *User) (name, avatarURL string) {
	member := portal.bridge.StateStore.GetMember(portal.MXID, sender.MXID)
	name = member.Displayname
	if name == "" {
		name = sender.MXID.String()
	}
	mxc := member.AvatarURL.ParseOrIgnore()
	if !mxc.IsEmpty() && portal.bridge.Config.Bridge.PublicAddress != "" {
		avatarURL = portal.bridge.makeMediaProxyURL(mxc)
	}
	return
}

const replyEmbedMaxLines = 1
const replyEmbedMaxChars = 72

func cutBody(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	var output string
	for i, line := range lines {
		if i >= replyEmbedMaxLines {
			output += " […]"
			break
		}
		if i > 0 {
			output += "\n"
		}
		output += line
		if len(output) > replyEmbedMaxChars {
			output = output[:replyEmbedMaxChars] + "…"
			break
		}
	}
	return output
}

func (portal *Portal) convertReplyMessageToEmbed(eventID id.EventID, url string) (*discordgo.MessageEmbed, error) {
	evt, err := portal.getEvent(eventID)
	if err != nil {
		return nil, fmt.Errorf("failed to get reply target event: %w", err)
	}
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil, fmt.Errorf("unsupported event type %s / %T", evt.Type.String(), evt.Content.Parsed)
	}
	content.RemoveReplyFallback()
	var targetUser string

	puppet := portal.bridge.GetPuppetByMXID(evt.Sender)
	if puppet != nil {
		targetUser = fmt.Sprintf("<@%s>", puppet.ID)
	} else if user := portal.bridge.GetUserByMXID(evt.Sender); user != nil && user.DiscordID != "" {
		targetUser = fmt.Sprintf("<@%s>", user.DiscordID)
	} else if member := portal.bridge.StateStore.GetMember(portal.MXID, evt.Sender); member != nil && member.Displayname != "" {
		targetUser = member.Displayname
	} else {
		targetUser = evt.Sender.String()
	}
	body := escapeDiscordMarkdown(cutBody(content.Body))
	body = fmt.Sprintf("**[Replying to](%s) %s**\n%s", url, targetUser, body)
	embed := &discordgo.MessageEmbed{Description: body}
	return embed, nil
}

func (portal *Portal) RefererOpt(threadID string) discordgo.RequestOption {
	if threadID != "" && threadID != portal.Key.ChannelID {
		return discordgo.WithThreadReferer(portal.GuildID, portal.Key.ChannelID, threadID)
	}
	return discordgo.WithChannelReferer(portal.GuildID, portal.Key.ChannelID)
}

func (portal *Portal) RefererOptIfUser(sess *discordgo.Session, threadID string) []discordgo.RequestOption {
	if sess == nil || !sess.IsUser {
		return nil
	}
	return []discordgo.RequestOption{portal.RefererOpt(threadID)}
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
	isWebhookSend := sess == nil
	var threadID string

	if editMXID := content.GetRelatesTo().GetReplaceID(); editMXID != "" && content.NewContent != nil {
		edits := portal.bridge.DB.Message.GetByMXID(portal.Key, editMXID)
		if edits != nil {
			discordContent, allowedMentions := portal.parseMatrixHTML(content.NewContent)
			var err error
			var msg *discordgo.Message
			if !isWebhookSend {
				// TODO save edit in message table
				msg, err = sess.ChannelMessageEdit(edits.DiscordProtoChannelID(), edits.DiscordID, discordContent)
			} else {
				msg, err = relayClient.WebhookMessageEdit(portal.RelayWebhookID, portal.RelayWebhookSecret, edits.DiscordID, &discordgo.WebhookEdit{
					Content:         &discordContent,
					AllowedMentions: allowedMentions,
				})
			}
			go portal.sendMessageMetrics(evt, err, "Failed to edit")
			if msg.EditedTimestamp != nil {
				edits.UpdateEditTimestamp(*msg.EditedTimestamp)
			}
		} else {
			go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errUnknownEditTarget, editMXID), "Ignoring")
		}
		return
	} else if threadRoot := content.GetRelatesTo().GetThreadParent(); threadRoot != "" {
		existingThread := portal.bridge.GetThreadByRootMXID(threadRoot)
		if existingThread != nil {
			threadID = existingThread.ID
			existingThread.initialBackfillAttempted = true
		} else {
			if isWebhookSend {
				// TODO start thread with bot?
				go portal.sendMessageMetrics(evt, errCantStartThread, "Dropping")
				return
			}
			var err error
			threadID, err = portal.startThreadFromMatrix(sender, threadRoot)
			if err != nil {
				portal.log.Warn().Err(err).
					Str("thread_root_mxid", threadRoot.String()).
					Msg("Failed to start thread from Matrix")
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

	replyToMXID := content.RelatesTo.GetNonFallbackReplyTo()
	var replyToUser id.UserID
	if replyToMXID != "" {
		replyTo := portal.bridge.DB.Message.GetByMXID(portal.Key, replyToMXID)
		if replyTo != nil && replyTo.ThreadID == threadID {
			replyToUser = replyTo.SenderMXID
			if isWebhookSend {
				messageURL := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", portal.GuildID, channelID, replyTo.DiscordID)
				embed, err := portal.convertReplyMessageToEmbed(replyTo.MXID, messageURL)
				if err != nil {
					portal.log.Warn().Err(err).Msg("Failed to convert reply message to embed for webhook send")
				} else if embed != nil {
					sendReq.Embeds = []*discordgo.MessageEmbed{embed}
				}
			} else {
				sendReq.Reference = &discordgo.MessageReference{
					ChannelID: channelID,
					MessageID: replyTo.DiscordID,
				}
			}
		}
	}
	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		sendReq.Content, sendReq.AllowedMentions = portal.parseMatrixHTML(content)
		if content.MsgType == event.MsgEmote {
			sendReq.Content = fmt.Sprintf("_%s_", sendReq.Content)
		}
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

		if portal.bridge.Config.Bridge.UseDiscordCDNUpload && !isWebhookSend && sess.IsUser {
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
			}, portal.RefererOpt(threadID))
			if err != nil {
				go portal.sendMessageMetrics(evt, err, "Error preparing to reupload media in")
				return
			}
			prepared := prep.Attachments[0]
			att.UploadedFilename = prepared.UploadFilename
			err = uploadDiscordAttachment(sender.Session.Client, prepared.UploadURL, data)
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
	silentReply := content.Mentions != nil && replyToMXID != "" &&
		(len(content.Mentions.UserIDs) == 0 || (replyToUser != "" && !slices.Contains(content.Mentions.UserIDs, replyToUser)))
	if silentReply && sendReq.AllowedMentions != nil {
		sendReq.AllowedMentions.RepliedUser = false
	}
	if !isWebhookSend {
		// AllowedMentions must not be set for real users, and it's also not that useful for personal bots.
		// It's only important for relaying, where the webhook may have higher permissions than the user on Matrix.
		if silentReply {
			sendReq.AllowedMentions = &discordgo.MessageAllowedMentions{
				Parse:       []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers, discordgo.AllowedMentionTypeRoles, discordgo.AllowedMentionTypeEveryone},
				RepliedUser: false,
			}
		} else {
			sendReq.AllowedMentions = nil
		}
	} else if strings.Contains(sendReq.Content, "@everyone") || strings.Contains(sendReq.Content, "@here") {
		powerLevels, err := portal.MainIntent().PowerLevels(portal.MXID)
		if err != nil {
			portal.log.Warn().Err(err).
				Str("user_id", sender.MXID.String()).
				Msg("Failed to get power levels to check if user can use @everyone")
		} else if powerLevels.GetUserLevel(sender.MXID) >= powerLevels.Notifications.Room() {
			sendReq.AllowedMentions.Parse = append(sendReq.AllowedMentions.Parse, discordgo.AllowedMentionTypeEveryone)
		}
	}
	sendReq.Nonce = generateNonce()
	var msg *discordgo.Message
	var err error
	if !isWebhookSend {
		msg, err = sess.ChannelMessageSendComplex(channelID, &sendReq, portal.RefererOptIfUser(sess, threadID)...)
	} else {
		username, avatarURL := portal.getRelayUserMeta(sender)
		msg, err = relayClient.WebhookThreadExecute(portal.RelayWebhookID, portal.RelayWebhookSecret, true, threadID, &discordgo.WebhookParams{
			Content:         sendReq.Content,
			Username:        username,
			AvatarURL:       avatarURL,
			Files:           sendReq.Files,
			Components:      sendReq.Components,
			Embeds:          sendReq.Embeds,
			AllowedMentions: sendReq.AllowedMentions,
		})
	}
	sender.handlePossible40002(err)
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
		dbMsg.SenderMXID = sender.MXID
		dbMsg.Timestamp, _ = discordgo.SnowflakeTimestamp(msg.ID)
		dbMsg.ThreadID = threadID
		dbMsg.Insert()
	}
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Warn().Err(err).
				Str("event_id", eventID.String()).
				Msg("Failed to send delivery receipt")
		}
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	sender := brSender.(*User)
	if portal.IsPrivateChat() && sender.DiscordID == portal.Key.Receiver {
		portal.log.Debug().Msg("User left private chat portal, cleaning up and deleting...")
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
		portal.log.Err(err).Msg("Failed to get Matrix user list to determine if portal needs to be cleaned up")
		return
	}

	if len(users) == 0 {
		portal.log.Info().Msg("Room seems to be empty, cleaning up...")
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
	portal.log = portal.bridge.ZLog.With().
		Str("channel_id", portal.Key.ChannelID).
		Str("channel_receiver", portal.Key.Receiver).
		Str("room_id", portal.MXID.String()).
		Logger()
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
	if portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureRoomYeeting) {
		err := intent.BeeperDeleteRoom(portal.MXID)
		if err != nil && !errors.Is(err, mautrix.MNotFound) {
			portal.log.Err(err).Msg("Failed to delete room using hungryserv yeet endpoint")
		}
		return
	}

	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warn().Err(err).Msg("Failed to leave private chat portal with main intent")
		}
		return
	}

	portal.bridge.cleanupRoom(intent, portal.MXID, puppetsOnly, portal.log)
}

func (br *DiscordBridge) cleanupRoom(intent *appservice.IntentAPI, mxid id.RoomID, puppetsOnly bool, log zerolog.Logger) {
	members, err := intent.JoinedMembers(mxid)
	if err != nil {
		log.Err(err).Msg("Failed to get portal members for cleanup")
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
				log.Err(err).Msg("Error leaving as puppet while cleaning up portal")
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(mxid, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				log.Err(err).Msg("Error kicking user while cleaning up portal")
			}
		}
	}

	_, err = intent.LeaveRoom(mxid)
	if err != nil {
		log.Err(err).Msg("Error leaving with main intent while cleaning up portal")
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
		emojiInfo := portal.bridge.DMA.GetEmojiInfo(uri)
		if emojiInfo != nil {
			emojiID = fmt.Sprintf("%s:%d", emojiInfo.Name, emojiInfo.EmojiID)
		} else if emojiFile := portal.bridge.DB.File.GetEmojiByMXC(uri); emojiFile != nil && emojiFile.ID != "" && emojiFile.EmojiName != "" {
			emojiID = fmt.Sprintf("%s:%s", emojiFile.EmojiName, emojiFile.ID)
		} else {
			go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errUnknownEmoji, emojiID), "Ignoring")
			return
		}
	} else {
		emojiID = variationselector.FullyQualify(emojiID)
	}

	existing := portal.bridge.DB.Reaction.GetByDiscordID(portal.Key, msg.DiscordID, sender.DiscordID, emojiID)
	if existing != nil {
		portal.log.Debug().
			Str("event_id", evt.ID.String()).
			Str("existing_reaction_mxid", existing.MXID.String()).
			Msg("Dropping duplicate Matrix reaction")
		go portal.sendMessageMetrics(evt, nil, "")
		return
	}

	err := sender.Session.MessageReactionAddUser(portal.GuildID, msg.DiscordProtoChannelID(), msg.DiscordID, emojiID)
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
		puppet.UpdateInfo(user, member.User, nil)
	}
	intent := puppet.IntentFor(portal)

	log := portal.log.With().
		Str("message_id", reaction.MessageID).
		Str("author_id", reaction.UserID).
		Bool("add", add).
		Str("action", "discord reaction").
		Logger()

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
		log.Debug().Msg("Failed to add reaction to message: message not found")
		return
	}

	// Lookup an existing reaction
	existing := portal.bridge.DB.Reaction.GetByDiscordID(portal.Key, message[0].DiscordID, reaction.UserID, discordID)
	if !add {
		if existing == nil {
			log.Debug().Msg("Failed to remove reaction: reaction not found")
			return
		}

		resp, err := intent.RedactEvent(portal.MXID, existing.MXID)
		if err != nil {
			log.Err(err).Msg("Failed to remove reaction")
		} else {
			go portal.sendDeliveryReceipt(resp.EventID)
		}

		existing.Delete()
		return
	} else if existing != nil {
		log.Debug().Msg("Ignoring duplicate reaction")
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
		wrappedShortcode := fmt.Sprintf(":%s:", reaction.Emoji.Name)
		extraContent["com.beeper.reaction.shortcode"] = wrappedShortcode
		if !portal.bridge.Config.Bridge.CustomEmojiReactions {
			content.RelatesTo.Key = wrappedShortcode
		}
	}

	resp, err := intent.SendMessageEvent(portal.MXID, event.EventReaction, &event.Content{
		Parsed: &content,
		Raw:    extraContent,
	})
	if err != nil {
		log.Err(err).Msg("Failed to send reaction")
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
			err = sess.ChannelMessageDelete(message.DiscordProtoChannelID(), message.DiscordID, portal.RefererOptIfUser(sess, message.ThreadID)...)
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
			err := sess.MessageReactionRemoveUser(portal.GuildID, reaction.DiscordProtoChannelID(), reaction.MessageID, reaction.EmojiName, reaction.Sender)
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
	log := portal.log.With().
		Str("sender", brUser.GetMXID().String()).
		Str("event_id", eventID.String()).
		Str("action", "matrix read receipt").
		Str("discord_thread_id", discordThreadID).
		Logger()
	if thread != nil {
		if portal.bridge.Config.Bridge.AutojoinThreadOnOpen {
			thread.Join(sender)
		}
		if eventID == thread.CreationNoticeMXID {
			log.Debug().Msg("Dropping read receipt for thread creation notice")
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
			log.Debug().Msg("Dropping read receipt: no messages found")
			return
		} else {
			log = log.With().
				Str("closest_event_id", msg.MXID.String()).
				Str("closest_message_id", msg.DiscordID).
				Logger()
			log.Debug().Msg("Read receipt target event not found, using closest message")
		}
	} else {
		log = log.With().
			Str("message_id", msg.DiscordID).
			Logger()
	}
	if receipt.ThreadID != "" && msg.ThreadID != discordThreadID {
		log.Debug().
			Str("receipt_thread_event_id", receipt.ThreadID.String()).
			Str("message_discord_thread_id", msg.ThreadID).
			Msg("Dropping read receipt: thread ID mismatch")
		return
	}
	resp, err := sender.Session.ChannelMessageAckNoToken(msg.DiscordProtoChannelID(), msg.DiscordID, portal.RefererOpt(msg.DiscordProtoChannelID()))
	if err != nil {
		log.Err(err).Msg("Failed to send read receipt to Discord")
	} else if resp.Token != nil {
		log.Debug().
			Str("unexpected_resp_token", *resp.Token).
			Msg("Marked message as read on Discord (and got unexpected non-nil token)")
	} else {
		log.Debug().Msg("Marked message as read on Discord")
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
			err := user.Session.ChannelTyping(portal.Key.ChannelID, portal.RefererOptIfUser(user.Session, "")...)
			if err != nil {
				portal.log.Warn().Err(err).
					Str("user_id", user.MXID.String()).
					Msg("Failed to mark user as typing")
			} else {
				portal.log.Debug().
					Str("user_id", user.MXID.String()).
					Msg("Marked user as typing")
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
	}), false) || plainNameChanged
}

func (portal *Portal) UpdateNameDirect(name string, isFriendNick bool) bool {
	if portal.FriendNick && !isFriendNick {
		return false
	} else if portal.Name == name && (portal.NameSet || portal.MXID == "" || (!portal.shouldSetDMRoomMetadata() && !isFriendNick)) {
		return false
	}
	portal.log.Debug().
		Str("old_name", portal.Name).
		Str("new_name", name).
		Msg("Updating portal name")
	portal.Name = name
	portal.NameSet = false
	portal.updateRoomName()
	return true
}

func (portal *Portal) updateRoomName() {
	if portal.MXID != "" && (portal.shouldSetDMRoomMetadata() || portal.FriendNick) {
		_, err := portal.MainIntent().SetRoomName(portal.MXID, portal.Name)
		if err != nil {
			portal.log.Err(err).Msg("Failed to update room name")
		} else {
			portal.NameSet = true
		}
	}
}

func (portal *Portal) UpdateAvatarFromPuppet(puppet *Puppet) bool {
	if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && (puppet.Avatar == "" || portal.AvatarSet || portal.MXID == "" || !portal.shouldSetDMRoomMetadata()) {
		return false
	}
	portal.log.Debug().
		Str("old_avatar_id", portal.Avatar).
		Str("new_avatar_id", puppet.Avatar).
		Msg("Updating avatar from puppet")
	portal.Avatar = puppet.Avatar
	portal.AvatarURL = puppet.AvatarURL
	portal.AvatarSet = false
	portal.updateRoomAvatar()
	return true
}

func (portal *Portal) UpdateGroupDMAvatar(iconID string) bool {
	if portal.Avatar == iconID && (iconID == "") == portal.AvatarURL.IsEmpty() && (iconID == "" || portal.AvatarSet || portal.MXID == "") {
		return false
	}
	portal.log.Debug().
		Str("old_avatar_id", portal.Avatar).
		Str("new_avatar_id", portal.Avatar).
		Msg("Updating group DM avatar")
	portal.Avatar = iconID
	portal.AvatarSet = false
	portal.AvatarURL = id.ContentURI{}
	if portal.Avatar != "" {
		// TODO direct media support
		copied, err := portal.bridge.copyAttachmentToMatrix(portal.MainIntent(), discordgo.EndpointGroupIcon(portal.Key.ChannelID, portal.Avatar), false, AttachmentMeta{
			AttachmentID: fmt.Sprintf("private_channel_avatar/%s/%s", portal.Key.ChannelID, iconID),
		})
		if err != nil {
			portal.log.Err(err).Str("avatar_id", iconID).Msg("Failed to reupload channel avatar")
			return true
		}
		portal.AvatarURL = copied.MXC
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
		portal.log.Err(err).Msg("Failed to update room avatar")
	} else {
		portal.AvatarSet = true
	}
}

func (portal *Portal) UpdateTopic(topic string) bool {
	if portal.Topic == topic && (portal.TopicSet || portal.MXID == "") {
		return false
	}
	portal.log.Debug().
		Str("old_topic", portal.Topic).
		Str("new_topic", topic).
		Msg("Updating portal topic")
	portal.Topic = topic
	portal.TopicSet = false
	portal.updateRoomTopic()
	return true
}

func (portal *Portal) updateRoomTopic() {
	if portal.MXID != "" {
		_, err := portal.MainIntent().SetRoomTopic(portal.MXID, portal.Topic)
		if err != nil {
			portal.log.Err(err).Msg("Failed to update room topic")
		} else {
			portal.TopicSet = true
		}
	}
}

func (portal *Portal) removeFromSpace() {
	if portal.InSpace == "" {
		return
	}

	log := portal.log.With().Str("space_mxid", portal.InSpace.String()).Logger()
	log.Debug().Msg("Removing room from space")
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, portal.InSpace.String(), struct{}{})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to clear m.space.parent event in room")
	}
	_, err = portal.bridge.Bot.SendStateEvent(portal.InSpace, event.StateSpaceChild, portal.MXID.String(), struct{}{})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to clear m.space.child event in space")
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

	log := portal.log.With().Str("space_mxid", mxid.String()).Logger()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, mxid.String(), &event.SpaceParentEventContent{
		Via:       []string{portal.bridge.AS.HomeserverDomain},
		Canonical: true,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to set m.space.parent event in room")
	}

	_, err = portal.bridge.Bot.SendStateEvent(mxid, event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{portal.bridge.AS.HomeserverDomain},
		// TODO order
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to set m.space.child event in space")
	} else {
		portal.InSpace = mxid
	}
	return true
}

func (portal *Portal) UpdateParent(parentID string) bool {
	if portal.ParentID == parentID {
		return false
	}
	portal.log.Debug().
		Str("old_parent_id", portal.ParentID).
		Str("new_parent_id", parentID).
		Msg("Updating parent ID")
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

func (portal *Portal) updateSpace(source *User) bool {
	if portal.MXID == "" {
		return false
	}
	if portal.Parent != nil {
		if portal.Parent.MXID != "" {
			portal.log.Warn().Str("parent_id", portal.ParentID).Msg("Parent portal has no Matrix room, creating...")
			err := portal.Parent.CreateMatrixRoom(source, nil)
			if err != nil {
				portal.log.Err(err).Str("parent_id", portal.ParentID).Msg("Failed to create Matrix room for parent")
				return false
			}
		}
		return portal.addToSpace(portal.Parent.MXID)
	} else if portal.Guild != nil {
		return portal.addToSpace(portal.Guild.MXID)
	}
	return false
}

func (portal *Portal) UpdateInfo(source *User, meta *discordgo.Channel) *discordgo.Channel {
	changed := false

	log := portal.log.With().
		Str("action", "update info").
		Str("through_user_mxid", source.MXID.String()).
		Str("through_user_dcid", source.DiscordID).
		Logger()

	if meta == nil {
		log.Debug().Msg("UpdateInfo called without metadata, fetching from user's state cache")
		meta, _ = source.Session.State.Channel(portal.Key.ChannelID)
		if meta == nil {
			log.Warn().Msg("No metadata found in state cache, fetching from server via user")
			var err error
			meta, err = source.Session.Channel(portal.Key.ChannelID)
			if err != nil {
				log.Err(err).Msg("Failed to fetch meta via user")
				return nil
			}
		}
	}

	if portal.Type != meta.Type {
		log.Warn().
			Int("old_type", int(portal.Type)).
			Int("new_type", int(meta.Type)).
			Msg("Portal type changed")
		portal.Type = meta.Type
		changed = true
	}
	if portal.OtherUserID == "" && portal.IsPrivateChat() {
		if len(meta.Recipients) == 0 {
			var err error
			meta, err = source.Session.Channel(meta.ID)
			if err != nil {
				log.Err(err).Msg("Failed to fetch DM channel info to find other user ID")
			}
		}
		if len(meta.Recipients) > 0 {
			portal.OtherUserID = meta.Recipients[0].ID
			log.Info().Str("other_user_id", portal.OtherUserID).Msg("Found other user ID")
			changed = true
		}
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
			if rel, ok := source.relationships[portal.OtherUserID]; ok && rel.Nickname != "" {
				portal.FriendNick = true
				changed = portal.UpdateNameDirect(rel.Nickname, true) || changed
			} else {
				portal.FriendNick = false
				changed = portal.UpdateNameDirect(puppet.Name, false) || changed
			}
		}
		if portal.MXID != "" {
			portal.syncParticipants(source, meta.Recipients)
		}
	case discordgo.ChannelTypeGroupDM:
		changed = portal.UpdateGroupDMAvatar(meta.Icon) || changed
		if portal.MXID != "" {
			portal.syncParticipants(source, meta.Recipients)
		}
		fallthrough
	default:
		changed = portal.UpdateName(meta) || changed
		if portal.MXID != "" {
			portal.ensureUserInvited(source, false)
		}
	}
	changed = portal.UpdateTopic(meta.Topic) || changed
	changed = portal.UpdateParent(meta.ParentID) || changed
	// Private channels are added to the space in User.handlePrivateChannel
	if portal.GuildID != "" && portal.MXID != "" && portal.ExpectedSpaceID() != portal.InSpace {
		changed = portal.updateSpace(source) || changed
	}
	if changed {
		portal.UpdateBridgeInfo()
		portal.Update()
	}
	return meta
}
