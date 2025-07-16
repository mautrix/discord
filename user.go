package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"

	"go.mau.fi/mautrix-discord/database"
)

var (
	ErrNotConnected = errors.New("not connected")
	ErrNotLoggedIn  = errors.New("not logged in")
)

type User struct {
	*database.User

	sync.Mutex

	bridge *DiscordBridge
	log    zerolog.Logger

	PermissionLevel bridgeconfig.PermissionLevel

	spaceCreateLock          sync.Mutex
	spaceMembershipChecked   bool
	dmSpaceMembershipChecked bool

	Session *discordgo.Session

	BridgeState     *bridge.BridgeStateQueue
	bridgeStateLock sync.Mutex
	wasDisconnected bool
	wasLoggedOut    bool

	markedOpened     map[string]time.Time
	markedOpenedLock sync.Mutex

	pendingInteractions     map[string]*WrappedCommandEvent
	pendingInteractionsLock sync.Mutex

	nextDiscordUploadID atomic.Int32

	relationships map[string]*discordgo.Relationship
}

func (user *User) GetRemoteID() string {
	return user.DiscordID
}

func (user *User) GetRemoteName() string {
	if user.Session != nil && user.Session.State != nil && user.Session.State.User != nil {
		if user.Session.State.User.Discriminator == "0" {
			return fmt.Sprintf("@%s", user.Session.State.User.Username)
		}
		return fmt.Sprintf("%s#%s", user.Session.State.User.Username, user.Session.State.User.Discriminator)
	}
	return user.DiscordID
}

var discordLog zerolog.Logger

func discordToZeroLevel(level int) zerolog.Level {
	switch level {
	case discordgo.LogError:
		return zerolog.ErrorLevel
	case discordgo.LogWarning:
		return zerolog.WarnLevel
	case discordgo.LogInformational:
		return zerolog.InfoLevel
	case discordgo.LogDebug:
		fallthrough
	default:
		return zerolog.DebugLevel
	}
}

func init() {
	discordgo.Logger = func(msgL, caller int, format string, a ...interface{}) {
		discordLog.WithLevel(discordToZeroLevel(msgL)).Caller(caller+1).Msgf(strings.TrimSpace(format), a...) // zerolog-allow-msgf
	}
}

func (user *User) GetPermissionLevel() bridgeconfig.PermissionLevel {
	return user.PermissionLevel
}

func (user *User) GetManagementRoomID() id.RoomID {
	return user.ManagementRoom
}

func (user *User) GetMXID() id.UserID {
	return user.MXID
}

func (user *User) GetCommandState() map[string]interface{} {
	return nil
}

func (user *User) GetIDoublePuppet() bridge.DoublePuppet {
	p := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if p == nil || p.CustomIntent() == nil {
		return nil
	}
	return p
}

func (user *User) GetIGhost() bridge.Ghost {
	if user.DiscordID == "" {
		return nil
	}
	p := user.bridge.GetPuppetByID(user.DiscordID)
	if p == nil {
		return nil
	}
	return p
}

var _ bridge.User = (*User)(nil)

func (br *DiscordBridge) loadUser(dbUser *database.User, mxid *id.UserID) *User {
	if dbUser == nil {
		if mxid == nil {
			return nil
		}
		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}

	user := br.NewUser(dbUser)
	br.usersByMXID[user.MXID] = user
	if user.DiscordID != "" {
		br.usersByID[user.DiscordID] = user
	}
	if user.ManagementRoom != "" {
		br.managementRoomsLock.Lock()
		br.managementRooms[user.ManagementRoom] = user
		br.managementRoomsLock.Unlock()
	}
	return user
}

func (br *DiscordBridge) GetUserByMXID(userID id.UserID) *User {
	if userID == br.Bot.UserID || br.IsGhost(userID) {
		return nil
	}
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByMXID[userID]
	if !ok {
		return br.loadUser(br.DB.User.GetByMXID(userID), &userID)
	}
	return user
}

func (br *DiscordBridge) GetUserByID(id string) *User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByID[id]
	if !ok {
		return br.loadUser(br.DB.User.GetByID(id), nil)
	}
	return user
}

func (br *DiscordBridge) GetCachedUserByID(id string) *User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()
	return br.usersByID[id]
}

func (br *DiscordBridge) GetCachedUserByMXID(userID id.UserID) *User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()
	return br.usersByMXID[userID]
}

func (br *DiscordBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.ZLog.With().Str("user_id", string(dbUser.MXID)).Logger(),

		markedOpened:    make(map[string]time.Time),
		PermissionLevel: br.Config.Bridge.Permissions.Get(dbUser.MXID),

		pendingInteractions: make(map[string]*WrappedCommandEvent),

		relationships: make(map[string]*discordgo.Relationship),
	}
	user.nextDiscordUploadID.Store(rand.Int31n(100))
	user.BridgeState = br.NewBridgeStateQueue(user)
	return user
}

func (br *DiscordBridge) getAllUsersWithToken() []*User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	dbUsers := br.DB.User.GetAllWithToken()
	users := make([]*User, len(dbUsers))

	for idx, dbUser := range dbUsers {
		user, ok := br.usersByMXID[dbUser.MXID]
		if !ok {
			user = br.loadUser(dbUser, nil)
		}
		users[idx] = user
	}
	return users
}

func (br *DiscordBridge) startUsers() {
	br.ZLog.Debug().Msg("Starting users")

	usersWithToken := br.getAllUsersWithToken()
	for _, u := range usersWithToken {
		go u.startupTryConnect(0)
	}
	if len(usersWithToken) == 0 {
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
	}

	br.ZLog.Debug().Msg("Starting custom puppets")
	for _, customPuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			br.ZLog.Debug().Str("user_id", puppet.CustomMXID.String()).Msg("Starting custom puppet")

			if err := puppet.StartCustomMXID(true); err != nil {
				puppet.log.Error().Err(err).Msg("Failed to start custom puppet")
			}
		}(customPuppet)
	}
}

func (user *User) startupTryConnect(retryCount int) {
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	err := user.Connect()
	if err != nil {
		user.log.Error().Err(err).Msg("Error connecting on startup")
		closeErr := &websocket.CloseError{}
		if errors.As(err, &closeErr) && closeErr.Code == 4004 {
			user.invalidAuthHandler(nil)
		} else if retryCount < 6 {
			user.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "dc-unknown-websocket-error", Message: err.Error()})
			retryInSeconds := 2 << retryCount
			user.log.Debug().Int("retry_in_seconds", retryInSeconds).Msg("Sleeping and retrying connection")
			time.Sleep(time.Duration(retryInSeconds) * time.Second)
			user.startupTryConnect(retryCount + 1)
		} else {
			user.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: "dc-unknown-websocket-error", Message: err.Error()})
		}
	}
}

func (user *User) SetManagementRoom(roomID id.RoomID) {
	user.bridge.managementRoomsLock.Lock()
	defer user.bridge.managementRoomsLock.Unlock()

	existing, ok := user.bridge.managementRooms[roomID]
	if ok {
		existing.ManagementRoom = ""
		existing.Update()
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	user.Update()
}

func (user *User) getSpaceRoom(ptr *id.RoomID, name, topic string, parent id.RoomID) id.RoomID {
	if len(*ptr) > 0 {
		return *ptr
	}
	user.spaceCreateLock.Lock()
	defer user.spaceCreateLock.Unlock()
	if len(*ptr) > 0 {
		return *ptr
	}

	initialState := []*event.Event{{
		Type: event.StateRoomAvatar,
		Content: event.Content{
			Parsed: &event.RoomAvatarEventContent{
				URL: user.bridge.Config.AppService.Bot.ParsedAvatar,
			},
		},
	}}

	if parent != "" {
		parentIDStr := parent.String()
		initialState = append(initialState, &event.Event{
			Type:     event.StateSpaceParent,
			StateKey: &parentIDStr,
			Content: event.Content{
				Parsed: &event.SpaceParentEventContent{
					Canonical: true,
					Via:       []string{user.bridge.AS.HomeserverDomain},
				},
			},
		})
	}

	resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:   "private",
		Name:         name,
		Topic:        topic,
		InitialState: initialState,
		CreationContent: map[string]interface{}{
			"type": event.RoomTypeSpace,
		},
		PowerLevelOverride: &event.PowerLevelsEventContent{
			Users: map[id.UserID]int{
				user.MXID: 50,
			},
		},
	})

	if err != nil {
		user.log.Error().Err(err).Msg("Failed to auto-create space room")
	} else {
		*ptr = resp.RoomID
		user.Update()
		user.ensureInvited(nil, *ptr, false, true)

		if parent != "" {
			_, err = user.bridge.Bot.SendStateEvent(parent, event.StateSpaceChild, resp.RoomID.String(), &event.SpaceChildEventContent{
				Via:   []string{user.bridge.AS.HomeserverDomain},
				Order: " 0000",
			})
			if err != nil {
				user.log.Error().Err(err).
					Str("created_space_id", resp.RoomID.String()).
					Str("parent_space_id", parent.String()).
					Msg("Failed to add created space room to parent space")
			}
		}
	}
	return *ptr
}

func (user *User) GetSpaceRoom() id.RoomID {
	return user.getSpaceRoom(&user.SpaceRoom, "Discord", "Your Discord bridged chats", "")
}

func (user *User) GetDMSpaceRoom() id.RoomID {
	return user.getSpaceRoom(&user.DMSpaceRoom, "Direct Messages", "Your Discord direct messages", user.GetSpaceRoom())
}

func (user *User) ViewingChannel(portal *Portal) bool {
	if portal.GuildID != "" || !user.Session.IsUser {
		return false
	}
	user.markedOpenedLock.Lock()
	defer user.markedOpenedLock.Unlock()
	ts := user.markedOpened[portal.Key.ChannelID]
	// TODO is there an expiry time?
	if ts.IsZero() {
		user.markedOpened[portal.Key.ChannelID] = time.Now()
		err := user.Session.MarkViewing(portal.Key.ChannelID)
		if err != nil {
			user.log.Error().Err(err).
				Str("channel_id", portal.Key.ChannelID).
				Msg("Failed to mark user as viewing channel")
		}
		return true
	}
	return false
}

func (user *User) mutePortal(intent *appservice.IntentAPI, portal *Portal, unmute bool) {
	if len(portal.MXID) == 0 || !user.bridge.Config.Bridge.MuteChannelsOnCreate {
		return
	}
	var err error
	if unmute {
		user.log.Debug().Str("room_id", portal.MXID.String()).Msg("Unmuting portal")
		err = intent.DeletePushRule("global", pushrules.RoomRule, string(portal.MXID))
	} else {
		user.log.Debug().Str("room_id", portal.MXID.String()).Msg("Muting portal")
		err = intent.PutPushRule("global", pushrules.RoomRule, string(portal.MXID), &mautrix.ReqPutPushRule{
			Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		})
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.log.Warn().Err(err).
			Str("room_id", portal.MXID.String()).
			Msg("Failed to update push rule through double puppet")
	}
}

func (user *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppetIntent := portal.bridge.GetPuppetByCustomMXID(user.MXID).CustomIntent()
	if doublePuppetIntent == nil || portal.MXID == "" {
		return
	}

	// TODO sync mute status properly
	if portal.GuildID != "" && user.bridge.Config.Bridge.MuteChannelsOnCreate && justCreated {
		user.mutePortal(doublePuppetIntent, portal, false)
	}
}

func (user *User) NextDiscordUploadID() string {
	val := user.nextDiscordUploadID.Add(2)
	return strconv.Itoa(int(val))
}

func (user *User) Login(token string) error {
	user.bridgeStateLock.Lock()
	user.wasLoggedOut = false
	user.bridgeStateLock.Unlock()
	user.DiscordToken = token
	var err error
	const maxRetries = 3
Loop:
	for i := 0; i < maxRetries; i++ {
		err = user.Connect()
		if err == nil {
			user.Update()
			return nil
		}
		user.log.Error().Err(err).Msg("Error connecting for login")
		closeErr := &websocket.CloseError{}
		errors.As(err, &closeErr)
		switch closeErr.Code {
		case 4004, 4010, 4011, 4012, 4013, 4014:
			break Loop
		case 4000:
			fallthrough
		default:
			if i < maxRetries-1 {
				time.Sleep(time.Duration(i+1) * 2 * time.Second)
			}
		}
	}
	user.DiscordToken = ""
	return err
}

func (user *User) IsLoggedIn() bool {
	user.Lock()
	defer user.Unlock()

	return user.DiscordToken != ""
}

func (user *User) Logout(isOverwriting bool) {
	user.Lock()
	defer user.Unlock()

	if user.DiscordID != "" {
		puppet := user.bridge.GetPuppetByID(user.DiscordID)
		if puppet.CustomMXID != "" {
			err := puppet.SwitchCustomMXID("", "")
			if err != nil {
				user.log.Warn().Err(err).Msg("Failed to disable custom puppet while logging out of Discord")
			}
		}
	}

	if user.Session != nil {
		if err := user.Session.Close(); err != nil {
			user.log.Warn().Err(err).Msg("Error closing session")
		}
	}

	user.Session = nil
	user.DiscordToken = ""
	user.ReadStateVersion = 0
	if !isOverwriting {
		user.bridge.usersLock.Lock()
		if user.bridge.usersByID[user.DiscordID] == user {
			delete(user.bridge.usersByID, user.DiscordID)
		}
		user.bridge.usersLock.Unlock()
	}
	user.DiscordID = ""
	user.Update()
	user.log.Info().Msg("User logged out")
}

func (user *User) Connected() bool {
	user.Lock()
	defer user.Unlock()

	return user.Session != nil
}

const BotIntents = discordgo.IntentGuilds |
	discordgo.IntentGuildMessages |
	discordgo.IntentGuildMessageReactions |
	discordgo.IntentGuildMessageTyping |
	discordgo.IntentGuildBans |
	discordgo.IntentGuildEmojis |
	discordgo.IntentGuildIntegrations |
	discordgo.IntentGuildInvites |
	//discordgo.IntentGuildVoiceStates |
	//discordgo.IntentGuildScheduledEvents |
	discordgo.IntentDirectMessages |
	discordgo.IntentDirectMessageTyping |
	discordgo.IntentDirectMessageTyping |
	// Privileged intents
	discordgo.IntentMessageContent |
	//discordgo.IntentGuildPresences |
	discordgo.IntentGuildMembers

func (user *User) Connect() error {
	user.Lock()
	defer user.Unlock()

	if user.DiscordToken == "" {
		return ErrNotLoggedIn
	}

	user.log.Debug().Msg("Connecting to discord")

	session, err := discordgo.New(user.DiscordToken)
	if err != nil {
		return err
	}
	if user.bridge.Config.Bridge.Proxy != "" {
		u, _ := url.Parse(user.bridge.Config.Bridge.Proxy)
		tlsConf := &tls.Config{
			InsecureSkipVerify: os.Getenv("DISCORD_SKIP_TLS_VERIFICATION") == "true",
		}
		session.Client.Transport = &http.Transport{
			Proxy:             http.ProxyURL(u),
			TLSClientConfig:   tlsConf,
			ForceAttemptHTTP2: true,
		}
		session.Dialer.Proxy = http.ProxyURL(u)
		session.Dialer.TLSClientConfig = tlsConf
	}
	// TODO move to config
	if os.Getenv("DISCORD_DEBUG") == "1" {
		session.LogLevel = discordgo.LogDebug
	} else {
		session.LogLevel = discordgo.LogInformational
	}
	userDiscordLog := user.log.With().Str("component", "discordgo").Logger()
	session.Logger = func(msgL, caller int, format string, a ...interface{}) {
		userDiscordLog.WithLevel(discordToZeroLevel(msgL)).Caller(caller+1).Msgf(strings.TrimSpace(format), a...) // zerolog-allow-msgf
	}
	if !session.IsUser {
		session.Identify.Intents = BotIntents
	}
	session.EventHandler = user.eventHandlerSync

	if session.IsUser {
		err = session.LoadMainPage(context.TODO())
		if err != nil {
			user.log.Warn().Err(err).Msg("Failed to load main page")
		}
	}

	user.Session = session

	for {
		err = user.Session.Open()
		if errors.Is(err, discordgo.ErrImmediateDisconnect) {
			user.log.Warn().Err(err).Msg("Retrying initial connection in 5 seconds")
			time.Sleep(5 * time.Second)
			continue
		}
		return err
	}
}

func (user *User) eventHandlerSync(rawEvt any) {
	go user.eventHandler(rawEvt)
}

func (user *User) eventHandler(rawEvt any) {
	defer func() {
		err := recover()
		if err != nil {
			user.log.Error().
				Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
				Any(zerolog.ErrorFieldName, err).
				Msg("Panic in Discord event handler")
		}
	}()
	switch evt := rawEvt.(type) {
	case *discordgo.Ready:
		user.readyHandler(evt)
	case *discordgo.Resumed:
		user.resumeHandler(evt)
	case *discordgo.Connect:
		user.connectedHandler(evt)
	case *discordgo.Disconnect:
		user.disconnectedHandler(evt)
	case *discordgo.InvalidAuth:
		user.invalidAuthHandler(evt)
	case *discordgo.GuildCreate:
		user.guildCreateHandler(evt)
	case *discordgo.GuildDelete:
		user.guildDeleteHandler(evt)
	case *discordgo.GuildUpdate:
		user.guildUpdateHandler(evt)
	case *discordgo.GuildRoleCreate:
		user.discordRoleToDB(evt.GuildID, evt.Role, nil, nil)
	case *discordgo.GuildRoleUpdate:
		user.discordRoleToDB(evt.GuildID, evt.Role, nil, nil)
	case *discordgo.GuildRoleDelete:
		user.bridge.DB.Role.DeleteByID(evt.GuildID, evt.RoleID)
	case *discordgo.ChannelCreate:
		user.channelCreateHandler(evt)
	case *discordgo.ChannelDelete:
		user.channelDeleteHandler(evt)
	case *discordgo.ChannelUpdate:
		user.channelUpdateHandler(evt)
	case *discordgo.ChannelRecipientAdd:
		user.channelRecipientAdd(evt)
	case *discordgo.ChannelRecipientRemove:
		user.channelRecipientRemove(evt)
	case *discordgo.RelationshipAdd:
		user.relationshipAddHandler(evt)
	case *discordgo.RelationshipRemove:
		user.relationshipRemoveHandler(evt)
	case *discordgo.RelationshipUpdate:
		user.relationshipUpdateHandler(evt)
	case *discordgo.MessageCreate:
		user.pushPortalMessage(evt, "message create", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageDelete:
		user.pushPortalMessage(evt, "message delete", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageDeleteBulk:
		user.pushPortalMessage(evt, "bulk message delete", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageUpdate:
		user.pushPortalMessage(evt, "message update", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageReactionAdd:
		user.pushPortalMessage(evt, "reaction add", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageReactionRemove:
		user.pushPortalMessage(evt, "reaction remove", evt.ChannelID, evt.GuildID)
	case *discordgo.MessageAck:
		user.messageAckHandler(evt)
	case *discordgo.TypingStart:
		user.typingStartHandler(evt)
	case *discordgo.InteractionSuccess:
		user.interactionSuccessHandler(evt)
	case *discordgo.ThreadListSync:
		user.threadListSyncHandler(evt)
	case *discordgo.Event:
		// Ignore
	default:
		user.log.Debug().Type("event_type", evt).Msg("Unhandled event")
	}
}

func (user *User) Disconnect() error {
	user.Lock()
	defer user.Unlock()
	if user.Session == nil {
		return ErrNotConnected
	}

	user.log.Info().Msg("Disconnecting session manually")
	if err := user.Session.Close(); err != nil {
		return err
	}
	user.Session = nil
	return nil
}

func (user *User) getGuildBridgingMode(guildID string) database.GuildBridgingMode {
	if guildID == "" {
		return database.GuildBridgeEverything
	}
	guild := user.bridge.GetGuildByID(guildID, false)
	if guild == nil {
		return database.GuildBridgeNothing
	}
	return guild.BridgingMode
}

type ChannelSlice []*discordgo.Channel

func (s ChannelSlice) Len() int {
	return len(s)
}

func (s ChannelSlice) Less(i, j int) bool {
	if s[i].Position != 0 || s[j].Position != 0 {
		return s[i].Position < s[j].Position
	}
	return compareMessageIDs(s[i].LastMessageID, s[j].LastMessageID) == 1
}

func (s ChannelSlice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (user *User) readyHandler(r *discordgo.Ready) {
	user.log.Debug().Msg("Discord connection ready")
	user.bridgeStateLock.Lock()
	user.wasLoggedOut = false
	user.bridgeStateLock.Unlock()

	if user.DiscordID != r.User.ID {
		user.bridge.usersLock.Lock()
		user.DiscordID = r.User.ID
		if previousUser, ok := user.bridge.usersByID[user.DiscordID]; ok && previousUser != user {
			user.log.Warn().
				Str("previous_user_id", previousUser.MXID.String()).
				Msg("Another user is logged in with same Discord ID, logging them out")
			// TODO send notice?
			previousUser.Logout(true)
		}
		user.bridge.usersByID[user.DiscordID] = user
		user.bridge.usersLock.Unlock()
		user.Update()
	}
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateBackfilling})
	user.tryAutomaticDoublePuppeting()

	for _, relationship := range r.Relationships {
		user.relationships[relationship.ID] = relationship
	}

	updateTS := time.Now()
	portalsInSpace := make(map[string]bool)
	for _, guild := range user.GetPortals() {
		portalsInSpace[guild.DiscordID] = guild.InSpace
	}
	for _, guild := range r.Guilds {
		user.handleGuild(guild, updateTS, portalsInSpace[guild.ID])
	}
	// The private channel list doesn't seem to be sorted by default, so sort it by message IDs (highest=newest first)
	sort.Sort(ChannelSlice(r.PrivateChannels))
	for i, ch := range r.PrivateChannels {
		portal := user.GetPortalByMeta(ch)
		user.handlePrivateChannel(portal, ch, updateTS, i < user.bridge.Config.Bridge.PrivateChannelCreateLimit, portalsInSpace[portal.Key.ChannelID])
	}
	user.PrunePortalList(updateTS)

	if r.ReadState != nil && r.ReadState.Version > user.ReadStateVersion {
		// TODO can we figure out which read states are actually new?
		for _, entry := range r.ReadState.Entries {
			user.messageAckHandler(&discordgo.MessageAck{
				MessageID: string(entry.LastMessageID),
				ChannelID: entry.ID,
			})
		}
		user.ReadStateVersion = r.ReadState.Version
		user.Update()
	}

	go user.subscribeGuilds(2 * time.Second)

	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

func (user *User) subscribeGuilds(delay time.Duration) {
	if !user.Session.IsUser {
		return
	}
	for _, guildMeta := range user.Session.State.Guilds {
		guild := user.bridge.GetGuildByID(guildMeta.ID, false)
		if guild != nil && guild.MXID != "" {
			user.log.Debug().Str("guild_id", guild.ID).Msg("Subscribing to guild")
			dat := discordgo.GuildSubscribeData{
				GuildID:    guild.ID,
				Typing:     true,
				Activities: true,
				Threads:    true,
			}
			err := user.Session.SubscribeGuild(dat)
			if err != nil {
				user.log.Warn().Err(err).Str("guild_id", guild.ID).Msg("Failed to subscribe to guild")
			}
			time.Sleep(delay)
		}
	}
}

func (user *User) resumeHandler(_ *discordgo.Resumed) {
	user.log.Debug().Msg("Discord connection resumed")
	user.subscribeGuilds(0 * time.Second)
}

func (user *User) addPrivateChannelToSpace(portal *Portal) bool {
	if portal.MXID == "" {
		return false
	}
	_, err := user.bridge.Bot.SendStateEvent(user.GetDMSpaceRoom(), event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{user.bridge.AS.HomeserverDomain},
	})
	if err != nil {
		user.log.Error().Err(err).
			Str("room_id", portal.MXID.String()).
			Msg("Failed to add DMM room to user DM space")
		return false
	} else {
		return true
	}
}

func (user *User) relationshipAddHandler(r *discordgo.RelationshipAdd) {
	user.log.Debug().Interface("relationship", r.Relationship).Msg("Relationship added")
	user.relationships[r.ID] = r.Relationship
	user.handleRelationshipChange(r.ID, r.Nickname)
}

func (user *User) relationshipUpdateHandler(r *discordgo.RelationshipUpdate) {
	user.log.Debug().Interface("relationship", r.Relationship).Msg("Relationship update")
	user.relationships[r.ID] = r.Relationship
	user.handleRelationshipChange(r.ID, r.Nickname)
}

func (user *User) relationshipRemoveHandler(r *discordgo.RelationshipRemove) {
	user.log.Debug().Str("other_user_id", r.ID).Msg("Relationship removed")
	delete(user.relationships, r.ID)
	user.handleRelationshipChange(r.ID, "")
}

func (user *User) handleRelationshipChange(userID, nickname string) {
	puppet := user.bridge.GetPuppetByID(userID)
	portal := user.FindPrivateChatWith(userID)
	if portal == nil || puppet == nil {
		return
	}

	updated := portal.FriendNick == (nickname != "")
	portal.FriendNick = nickname != ""
	if nickname != "" {
		updated = portal.UpdateNameDirect(nickname, true)
	} else if portal.Name != puppet.Name {
		if portal.shouldSetDMRoomMetadata() {
			updated = portal.UpdateNameDirect(puppet.Name, false)
		} else if portal.NameSet {
			_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateRoomName, "", map[string]any{})
			if err != nil {
				portal.log.Warn().Err(err).Msg("Failed to clear room name after friend nickname was removed")
			} else {
				portal.log.Debug().Msg("Cleared room name after friend nickname was removed")
				portal.NameSet = false
				portal.Update()
				updated = true
			}
		}
	}
	if !updated {
		portal.Update()
	}
}

func (user *User) handlePrivateChannel(portal *Portal, meta *discordgo.Channel, timestamp time.Time, create, isInSpace bool) {
	if create && portal.MXID == "" {
		err := portal.CreateMatrixRoom(user, meta)
		if err != nil {
			user.log.Error().Err(err).
				Str("channel_id", portal.Key.ChannelID).
				Msg("Failed to create portal for private channel in create handler")
		}
	} else {
		portal.UpdateInfo(user, meta)
		portal.ForwardBackfillMissed(user, meta.LastMessageID, nil)
	}
	user.MarkInPortal(database.UserPortal{
		DiscordID: portal.Key.ChannelID,
		Type:      database.UserPortalTypeDM,
		Timestamp: timestamp,
		InSpace:   isInSpace || user.addPrivateChannelToSpace(portal),
	})
}

func (user *User) addGuildToSpace(guild *Guild, isInSpace bool, timestamp time.Time) bool {
	if len(guild.MXID) > 0 && !isInSpace {
		_, err := user.bridge.Bot.SendStateEvent(user.GetSpaceRoom(), event.StateSpaceChild, guild.MXID.String(), &event.SpaceChildEventContent{
			Via: []string{user.bridge.AS.HomeserverDomain},
		})
		if err != nil {
			user.log.Error().Err(err).
				Str("guild_space_id", guild.MXID.String()).
				Msg("Failed to add guild space to user space")
		} else {
			isInSpace = true
		}
	}
	user.MarkInPortal(database.UserPortal{
		DiscordID: guild.ID,
		Type:      database.UserPortalTypeGuild,
		Timestamp: timestamp,
		InSpace:   isInSpace,
	})
	return isInSpace
}

func (user *User) discordRoleToDB(guildID string, role *discordgo.Role, dbRole *database.Role, txn dbutil.Execable) bool {
	var changed bool
	if dbRole == nil {
		dbRole = user.bridge.DB.Role.New()
		dbRole.ID = role.ID
		dbRole.GuildID = guildID
		changed = true
	} else {
		changed = dbRole.Name != role.Name ||
			dbRole.Icon != role.Icon ||
			dbRole.Mentionable != role.Mentionable ||
			dbRole.Managed != role.Managed ||
			dbRole.Hoist != role.Hoist ||
			dbRole.Color != role.Color ||
			dbRole.Position != role.Position ||
			dbRole.Permissions != role.Permissions
	}
	dbRole.Role = *role
	if changed {
		dbRole.Upsert(txn)
	}
	return changed
}

func (user *User) handleGuildRoles(guildID string, newRoles []*discordgo.Role) {
	existingRoles := user.bridge.DB.Role.GetAll(guildID)
	existingRoleMap := make(map[string]*database.Role, len(existingRoles))
	for _, role := range existingRoles {
		existingRoleMap[role.ID] = role
	}
	txn, err := user.bridge.DB.Begin()
	if err != nil {
		user.log.Error().Err(err).Msg("Failed to start transaction for guild role sync")
		panic(err)
	}
	for _, role := range newRoles {
		user.discordRoleToDB(guildID, role, existingRoleMap[role.ID], txn)
		delete(existingRoleMap, role.ID)
	}
	for _, removeRole := range existingRoleMap {
		removeRole.Delete(txn)
	}
	err = txn.Commit()
	if err != nil {
		user.log.Error().Err(err).Msg("Failed to commit guild role sync transaction")
		rollbackErr := txn.Rollback()
		if rollbackErr != nil {
			user.log.Error().Err(rollbackErr).Msg("Failed to rollback errored guild role sync transaction")
		}
		panic(err)
	}
}

func (user *User) handleGuild(meta *discordgo.Guild, timestamp time.Time, isInSpace bool) {
	guild := user.bridge.GetGuildByID(meta.ID, true)
	guild.UpdateInfo(user, meta)
	if len(meta.Channels) > 0 {
		for _, ch := range meta.Channels {
			if !user.channelIsBridgeable(ch) {
				continue
			}
			portal := user.GetPortalByMeta(ch)
			if guild.BridgingMode >= database.GuildBridgeEverything && portal.MXID == "" {
				err := portal.CreateMatrixRoom(user, ch)
				if err != nil {
					user.log.Error().Err(err).
						Str("guild_id", guild.ID).
						Str("channel_id", ch.ID).
						Msg("Failed to create portal for guild channel in guild handler")
				}
			} else {
				portal.UpdateInfo(user, ch)
				if user.bridge.Config.Bridge.Backfill.MaxGuildMembers < 0 || meta.MemberCount < user.bridge.Config.Bridge.Backfill.MaxGuildMembers {
					portal.ForwardBackfillMissed(user, ch.LastMessageID, nil)
				}
			}
		}
	}
	if len(meta.Roles) > 0 {
		user.handleGuildRoles(meta.ID, meta.Roles)
	}
	user.addGuildToSpace(guild, isInSpace, timestamp)
}

func (user *User) connectedHandler(_ *discordgo.Connect) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	user.log.Debug().Msg("Connected to Discord")
	if user.wasDisconnected {
		user.wasDisconnected = false
		user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	}
}

func (user *User) disconnectedHandler(_ *discordgo.Disconnect) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	if user.wasLoggedOut {
		user.log.Debug().Msg("Disconnected from Discord (not updating bridge state as user was just logged out)")
		return
	}
	user.log.Debug().Msg("Disconnected from Discord")
	user.wasDisconnected = true
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "dc-transient-disconnect", Message: "Temporarily disconnected from Discord, trying to reconnect"})
}

func (user *User) invalidAuthHandler(_ *discordgo.InvalidAuth) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	user.log.Info().Msg("Got logged out from Discord due to invalid token")
	user.wasLoggedOut = true
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Error: "dc-websocket-disconnect-4004", Message: "Discord access token is no longer valid, please log in again"})
	go user.Logout(false)
}

func (user *User) handlePossible40002(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Message == nil || restErr.Message.Code != discordgo.ErrCodeActionRequiredVerifiedAccount {
		return false
	}
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Error: "dc-http-40002", Message: restErr.Message.Message})
	return true
}

func (user *User) guildCreateHandler(g *discordgo.GuildCreate) {
	user.log.Info().
		Str("guild_id", g.ID).
		Str("name", g.Name).
		Bool("unavailable", g.Unavailable).
		Msg("Got guild create event")
	user.handleGuild(g.Guild, time.Now(), false)
}

func (user *User) guildDeleteHandler(g *discordgo.GuildDelete) {
	if g.Unavailable {
		user.log.Info().Str("guild_id", g.ID).Msg("Ignoring guild delete event with unavailable flag")
		return
	}
	user.log.Info().Str("guild_id", g.ID).Msg("Got guild delete event")
	user.MarkNotInPortal(g.ID)
	guild := user.bridge.GetGuildByID(g.ID, false)
	if guild == nil || guild.MXID == "" {
		return
	}
	if user.bridge.Config.Bridge.DeleteGuildOnLeave && !user.PortalHasOtherUsers(g.ID) {
		user.log.Debug().Str("guild_id", g.ID).Msg("No other users in guild, cleaning up all portals")
		err := user.unbridgeGuild(g.ID)
		if err != nil {
			user.log.Warn().Err(err).Msg("Failed to unbridge guild that was deleted")
		}
	}
}

func (user *User) guildUpdateHandler(g *discordgo.GuildUpdate) {
	user.log.Debug().Str("guild_id", g.ID).Msg("Got guild update event")
	user.handleGuild(g.Guild, time.Now(), user.IsInSpace(g.ID))
}

func (user *User) threadListSyncHandler(t *discordgo.ThreadListSync) {
	for _, meta := range t.Threads {
		log := user.log.With().
			Str("action", "thread list sync").
			Str("guild_id", t.GuildID).
			Str("parent_id", meta.ParentID).
			Str("thread_id", meta.ID).
			Logger()
		ctx := log.WithContext(context.Background())
		thread := user.bridge.GetThreadByID(meta.ID, nil)
		if thread == nil {
			msg := user.bridge.DB.Message.GetByDiscordID(database.NewPortalKey(meta.ParentID, ""), meta.ID)
			if len(msg) == 0 {
				log.Debug().Msg("Found unknown thread in thread list sync and don't have message")
			} else {
				log.Debug().Msg("Found unknown thread in thread list sync for existing message, creating thread")
				user.bridge.threadFound(ctx, user, msg[0], meta.ID, meta)
			}
		} else {
			thread.Parent.ForwardBackfillMissed(user, meta.LastMessageID, thread)
		}
	}
}

func (user *User) channelCreateHandler(c *discordgo.ChannelCreate) {
	if user.getGuildBridgingMode(c.GuildID) < database.GuildBridgeEverything {
		user.log.Debug().
			Str("guild_id", c.GuildID).Str("channel_id", c.ID).
			Msg("Ignoring channel create event in unbridged guild")
		return
	}
	user.log.Info().
		Str("guild_id", c.GuildID).Str("channel_id", c.ID).
		Msg("Got channel create event")
	portal := user.GetPortalByMeta(c.Channel)
	if portal.MXID != "" {
		return
	}
	if c.GuildID == "" {
		user.handlePrivateChannel(portal, c.Channel, time.Now(), true, user.IsInSpace(portal.Key.String()))
	} else if user.channelIsBridgeable(c.Channel) {
		err := portal.CreateMatrixRoom(user, c.Channel)
		if err != nil {
			user.log.Error().Err(err).
				Str("guild_id", c.GuildID).Str("channel_id", c.ID).
				Msg("Error creating Matrix room after channel create event")
		}
	} else {
		user.log.Debug().
			Str("guild_id", c.GuildID).Str("channel_id", c.ID).
			Msg("Got channel create event, but it's not bridgeable, ignoring")
	}
}

func (user *User) channelDeleteHandler(c *discordgo.ChannelDelete) {
	portal := user.GetExistingPortalByID(c.ID)
	if portal == nil {
		user.log.Debug().
			Str("guild_id", c.GuildID).Str("channel_id", c.ID).
			Msg("Ignoring channel delete event of unknown channel")
		return
	}
	user.log.Info().
		Str("guild_id", c.GuildID).Str("channel_id", c.ID).
		Msg("Got channel delete event, cleaning up portal")
	portal.Delete()
	portal.cleanup(!user.bridge.Config.Bridge.DeletePortalOnChannelDelete)
	if c.GuildID == "" {
		user.MarkNotInPortal(portal.Key.ChannelID)
	}
	user.log.Debug().
		Str("guild_id", c.GuildID).Str("channel_id", c.ID).
		Msg("Completed cleaning up channel")
}

func (user *User) channelUpdateHandler(c *discordgo.ChannelUpdate) {
	portal := user.GetPortalByMeta(c.Channel)
	if c.GuildID == "" {
		user.handlePrivateChannel(portal, c.Channel, time.Now(), true, user.IsInSpace(portal.Key.String()))
	} else if user.channelIsBridgeable(c.Channel) {
		portal.UpdateInfo(user, c.Channel)
	}
}

func (user *User) channelRecipientAdd(c *discordgo.ChannelRecipientAdd) {
	portal := user.GetExistingPortalByID(c.ChannelID)
	if portal != nil {
		portal.syncParticipant(user, c.User, false)
	}
}

func (user *User) channelRecipientRemove(c *discordgo.ChannelRecipientRemove) {
	portal := user.GetExistingPortalByID(c.ChannelID)
	if portal != nil {
		portal.syncParticipant(user, c.User, true)
	}
}

func (user *User) findPortal(channelID string) (*Portal, *Thread) {
	portal := user.GetExistingPortalByID(channelID)
	if portal != nil {
		return portal, nil
	}
	thread := user.bridge.GetThreadByID(channelID, nil)
	if thread != nil && thread.Parent != nil {
		return thread.Parent, thread
	}
	if !user.Session.IsUser {
		channel, _ := user.Session.State.Channel(channelID)
		if channel == nil {
			user.log.Debug().Str("channel_id", channelID).Msg("Fetching info of unknown channel to handle message")
			var err error
			channel, err = user.Session.Channel(channelID)
			if err != nil {
				user.log.Warn().Err(err).Str("channel_id", channelID).Msg("Failed to get info of unknown channel")
			} else {
				user.log.Debug().Str("channel_id", channelID).Msg("Got info for channel to handle message")
				_ = user.Session.State.ChannelAdd(channel)
			}
		}
		if channel != nil && user.channelIsBridgeable(channel) {
			user.log.Debug().Str("channel_id", channelID).Msg("Creating portal and updating info to handle message")
			portal = user.GetPortalByMeta(channel)
			if channel.GuildID == "" {
				user.handlePrivateChannel(portal, channel, time.Now(), false, false)
			} else {
				user.log.Warn().
					Str("channel_id", channel.ID).Str("guild_id", channel.GuildID).
					Msg("Unexpected unknown guild channel")
			}
			return portal, nil
		}
	}
	return nil, nil
}

func (user *User) pushPortalMessage(msg interface{}, typeName, channelID, guildID string) {
	if user.getGuildBridgingMode(guildID) <= database.GuildBridgeNothing {
		// If guild bridging mode is nothing, don't even check if the portal exists
		return
	}

	portal, thread := user.findPortal(channelID)
	if portal == nil {
		user.log.Debug().
			Str("discord_event", typeName).
			Str("guild_id", guildID).
			Str("channel_id", channelID).
			Msg("Dropping event in unknown channel")
		return
	}
	if mode := user.getGuildBridgingMode(portal.GuildID); mode <= database.GuildBridgeNothing || (portal.MXID == "" && mode <= database.GuildBridgeIfPortalExists) {
		return
	}

	wrappedMsg := portalDiscordMessage{
		msg:    msg,
		user:   user,
		thread: thread,
	}
	select {
	case portal.discordMessages <- wrappedMsg:
	default:
		user.log.Warn().
			Str("discord_event", typeName).
			Str("guild_id", guildID).
			Str("channel_id", channelID).
			Msg("Portal message buffer is full")
		portal.discordMessages <- wrappedMsg
	}
}

type CustomReadReceipt struct {
	Timestamp          int64  `json:"ts,omitempty"`
	DoublePuppetSource string `json:"fi.mau.double_puppet_source,omitempty"`
}

type CustomReadMarkers struct {
	mautrix.ReqSetReadMarkers
	ReadExtra      CustomReadReceipt `json:"com.beeper.read.extra"`
	FullyReadExtra CustomReadReceipt `json:"com.beeper.fully_read.extra"`
}

func (user *User) makeReadMarkerContent(eventID id.EventID) *CustomReadMarkers {
	var extra CustomReadReceipt
	extra.DoublePuppetSource = user.bridge.Name
	return &CustomReadMarkers{
		ReqSetReadMarkers: mautrix.ReqSetReadMarkers{
			Read:      eventID,
			FullyRead: eventID,
		},
		ReadExtra:      extra,
		FullyReadExtra: extra,
	}
}

func (user *User) messageAckHandler(m *discordgo.MessageAck) {
	portal := user.GetExistingPortalByID(m.ChannelID)
	if portal == nil || portal.MXID == "" {
		return
	}
	dp := user.GetIDoublePuppet()
	if dp == nil {
		return
	}
	msg := user.bridge.DB.Message.GetLastByDiscordID(portal.Key, m.MessageID)
	if msg == nil {
		user.log.Debug().
			Str("channel_id", m.ChannelID).Str("message_id", m.MessageID).
			Msg("Dropping message ack event for unknown message")
		return
	}
	err := dp.CustomIntent().SetReadMarkers(portal.MXID, user.makeReadMarkerContent(msg.MXID))
	if err != nil {
		user.log.Error().Err(err).
			Str("event_id", msg.MXID.String()).Str("message_id", msg.DiscordID).
			Msg("Failed to mark event as read")
	} else {
		user.log.Debug().
			Str("event_id", msg.MXID.String()).Str("message_id", msg.DiscordID).
			Msg("Marked event as read after Discord message ack")
		if user.ReadStateVersion < m.Version {
			user.ReadStateVersion = m.Version
			// TODO maybe don't update every time?
			user.Update()
		}
	}
}

func (user *User) typingStartHandler(t *discordgo.TypingStart) {
	if t.UserID == user.DiscordID {
		return
	}
	portal := user.GetExistingPortalByID(t.ChannelID)
	if portal == nil || portal.MXID == "" {
		return
	}
	targetUser := user.bridge.GetCachedUserByID(t.UserID)
	if targetUser != nil {
		return
	}
	portal.handleDiscordTyping(t)
}

func (user *User) interactionSuccessHandler(s *discordgo.InteractionSuccess) {
	user.pendingInteractionsLock.Lock()
	defer user.pendingInteractionsLock.Unlock()
	ce, ok := user.pendingInteractions[s.Nonce]
	if !ok {
		user.log.Debug().Str("nonce", s.Nonce).Str("id", s.ID).Msg("Got interaction success for unknown interaction")
	} else {
		user.log.Debug().Str("nonce", s.Nonce).Str("id", s.ID).Msg("Got interaction success for pending interaction")
		ce.React("✅")
		delete(user.pendingInteractions, s.Nonce)
	}
}

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect, ignoreCache bool) bool {
	if roomID == "" {
		return false
	}
	if intent == nil {
		intent = user.bridge.Bot
	}
	if !ignoreCache && intent.StateStore.IsInvited(roomID, user.MXID) {
		return true
	}
	ret := false

	inviteContent := event.Content{
		Parsed: &event.MemberEventContent{
			Membership: event.MembershipInvite,
			IsDirect:   isDirect,
		},
		Raw: map[string]interface{}{},
	}

	customPuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		inviteContent.Raw["fi.mau.will_auto_accept"] = true
	}

	_, err := intent.SendStateEvent(roomID, event.StateMember, user.MXID.String(), &inviteContent)

	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
		ret = true
	} else if err != nil {
		user.log.Error().Err(err).Str("room_id", roomID.String()).Msg("Failed to invite user to room")
	} else {
		ret = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.log.Warn().Err(err).Str("room_id", roomID.String()).Msg("Failed to auto-join room")
			ret = false
		} else {
			ret = true
		}
	}

	return ret
}

func (user *User) getDirectChats() map[id.UserID][]id.RoomID {
	chats := map[id.UserID][]id.RoomID{}

	privateChats := user.bridge.DB.Portal.FindPrivateChatsOf(user.DiscordID)
	for _, portal := range privateChats {
		if portal.MXID != "" {
			puppetMXID := user.bridge.FormatPuppetMXID(portal.Key.Receiver)

			chats[puppetMXID] = []id.RoomID{portal.MXID}
		}
	}

	return chats
}

func (user *User) updateDirectChats(chats map[id.UserID][]id.RoomID) {
	if !user.bridge.Config.Bridge.SyncDirectChatList {
		return
	}

	puppet := user.bridge.GetPuppetByMXID(user.MXID)
	if puppet == nil {
		return
	}

	intent := puppet.CustomIntent()
	if intent == nil {
		return
	}

	method := http.MethodPatch
	if chats == nil {
		chats = user.getDirectChats()
		method = http.MethodPut
	}

	user.log.Debug().Msg("Updating m.direct list on homeserver")

	var err error
	if user.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareAsmux {
		urlPath := intent.BuildURL(mautrix.ClientURLPath{"unstable", "com.beeper.asmux", "dms"})
		_, err = intent.MakeFullRequest(mautrix.FullRequest{
			Method:      method,
			URL:         urlPath,
			Headers:     http.Header{"X-Asmux-Auth": {user.bridge.AS.Registration.AppToken}},
			RequestJSON: chats,
		})
	} else {
		existingChats := map[id.UserID][]id.RoomID{}

		err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
		if err != nil {
			user.log.Warn().Err(err).Msg("Failed to get m.direct event to update it")
			return
		}

		for userID, rooms := range existingChats {
			if _, ok := user.bridge.ParsePuppetMXID(userID); !ok {
				// This is not a ghost user, include it in the new list
				chats[userID] = rooms
			} else if _, ok := chats[userID]; !ok && method == http.MethodPatch {
				// This is a ghost user, but we're not replacing the whole list, so include it too
				chats[userID] = rooms
			}
		}

		err = intent.SetAccountData(event.AccountDataDirectChats.Type, &chats)
	}

	if err != nil {
		user.log.Warn().Err(err).Msg("Failed to update m.direct event")
	}
}

func (user *User) bridgeGuild(guildID string, everything bool) error {
	guild := user.bridge.GetGuildByID(guildID, false)
	if guild == nil {
		return errors.New("guild not found")
	}
	meta, _ := user.Session.State.Guild(guildID)
	err := guild.CreateMatrixRoom(user, meta)
	if err != nil {
		return err
	}
	log := user.log.With().Str("guild_id", guild.ID).Logger()
	user.addGuildToSpace(guild, false, time.Now())
	for _, ch := range meta.Channels {
		portal := user.GetPortalByMeta(ch)
		if (everything && user.channelIsBridgeable(ch)) || ch.Type == discordgo.ChannelTypeGuildCategory {
			err = portal.CreateMatrixRoom(user, ch)
			if err != nil {
				log.Error().Err(err).Str("channel_id", ch.ID).
					Msg("Failed to create room for guild channel while bridging guild")
			}
		}
	}
	if everything {
		guild.BridgingMode = database.GuildBridgeEverything
	} else {
		guild.BridgingMode = database.GuildBridgeCreateOnMessage
	}
	guild.Update()

	if user.Session.IsUser {
		log.Debug().Msg("Subscribing to guild after bridging")
		err = user.Session.SubscribeGuild(discordgo.GuildSubscribeData{
			GuildID:    guild.ID,
			Typing:     true,
			Activities: true,
			Threads:    true,
		})
		if err != nil {
			log.Warn().Err(err).Msg("Failed to subscribe to guild")
		}
	}

	return nil
}

func (user *User) unbridgeGuild(guildID string) error {
	if user.PermissionLevel < bridgeconfig.PermissionLevelAdmin && user.PortalHasOtherUsers(guildID) {
		return errors.New("only bridge admins can unbridge guilds with other users")
	}
	guild := user.bridge.GetGuildByID(guildID, false)
	if guild == nil {
		return errors.New("guild not found")
	}
	guild.roomCreateLock.Lock()
	defer guild.roomCreateLock.Unlock()
	if guild.BridgingMode == database.GuildBridgeNothing && guild.MXID == "" {
		return errors.New("that guild is not bridged")
	}
	guild.BridgingMode = database.GuildBridgeNothing
	guild.Update()
	for _, portal := range user.bridge.GetAllPortalsInGuild(guild.ID) {
		portal.cleanup(false)
		portal.RemoveMXID()
	}
	guild.cleanup()
	guild.RemoveMXID()
	return nil
}
