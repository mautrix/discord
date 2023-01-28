package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	log "maunium.net/go/maulogger/v2"

	"github.com/bwmarrin/discordgo"

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
	log    log.Logger

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
}

func (user *User) GetRemoteID() string {
	return user.DiscordID
}

func (user *User) GetRemoteName() string {
	if user.Session != nil && user.Session.State != nil && user.Session.State.User != nil {
		return fmt.Sprintf("%s#%s", user.Session.State.User.Username, user.Session.State.User.Discriminator)
	}
	return user.DiscordID
}

var discordLog log.Logger

func init() {
	discordgo.Logger = func(msgL, caller int, format string, a ...interface{}) {
		pc, file, line, _ := runtime.Caller(caller + 1)

		files := strings.Split(file, "/")
		file = files[len(files)-1]

		name := runtime.FuncForPC(pc).Name()
		fns := strings.Split(name, ".")
		name = fns[len(fns)-1]

		msg := fmt.Sprintf(format, a...)

		var level log.Level
		switch msgL {
		case discordgo.LogError:
			level = log.LevelError
		case discordgo.LogWarning:
			level = log.LevelWarn
		case discordgo.LogInformational:
			level = log.LevelInfo
		case discordgo.LogDebug:
			level = log.LevelDebug
		}

		discordLog.Logfln(level, "%s:%d:%s() %s", file, line, name, msg)
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

func (br *DiscordBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.Log.Sub("User").Sub(string(dbUser.MXID)),

		markedOpened:    make(map[string]time.Time),
		PermissionLevel: br.Config.Bridge.Permissions.Get(dbUser.MXID),
	}
	user.BridgeState = br.NewBridgeStateQueue(user, user.log)
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
	br.Log.Debugln("Starting users")

	usersWithToken := br.getAllUsersWithToken()
	for _, u := range usersWithToken {
		go u.startupTryConnect(0)
	}
	if len(usersWithToken) == 0 {
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
	}

	br.Log.Debugln("Starting custom puppets")
	for _, customPuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			br.Log.Debugln("Starting custom puppet", puppet.CustomMXID)

			if err := puppet.StartCustomMXID(true); err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
			}
		}(customPuppet)
	}
}

func (user *User) startupTryConnect(retryCount int) {
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	err := user.Connect()
	if err != nil {
		user.log.Errorfln("Error connecting: %v", err)
		closeErr := &websocket.CloseError{}
		if errors.As(err, &closeErr) && closeErr.Code == 4004 {
			user.invalidAuthHandler(nil, nil)
		} else if retryCount < 6 {
			user.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "dc-unknown-websocket-error", Message: err.Error()})
			retryInSeconds := 2 << retryCount
			user.log.Debugfln("Retrying connection in %d seconds", retryInSeconds)
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
				user.bridge.Bot.UserID: 9001,
				user.MXID:              50,
			},
		},
	})

	if err != nil {
		user.log.Errorln("Failed to auto-create space room:", err)
	} else {
		*ptr = resp.RoomID
		user.Update()
		user.ensureInvited(nil, *ptr, false)

		if parent != "" {
			_, err = user.bridge.Bot.SendStateEvent(parent, event.StateSpaceChild, resp.RoomID.String(), &event.SpaceChildEventContent{
				Via:   []string{user.bridge.AS.HomeserverDomain},
				Order: " 0000",
			})
			if err != nil {
				user.log.Errorfln("Failed to add space room %s to parent space %s: %v", resp.RoomID, parent, err)
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

func (user *User) tryAutomaticDoublePuppeting() {
	user.Lock()
	defer user.Unlock()

	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}

	user.log.Debugln("Checking if double puppeting needs to be enabled")

	puppet := user.bridge.GetPuppetByID(user.DiscordID)
	if puppet.CustomMXID != "" {
		user.log.Debugln("User already has double-puppeting enabled")

		return
	}

	accessToken, err := puppet.loginWithSharedSecret(user.MXID)
	if err != nil {
		user.log.Warnln("Failed to login with shared secret:", err)

		return
	}

	err = puppet.SwitchCustomMXID(accessToken, user.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)

		return
	}

	user.log.Infoln("Successfully automatically enabled custom puppet")
}

func (user *User) ViewingChannel(portal *Portal) bool {
	if portal.GuildID != "" {
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
			user.log.Errorfln("Failed to mark user as viewing %s: %v", portal.Key.ChannelID, err)
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
		user.log.Debugfln("Unmuting portal %s", portal.MXID)
		err = intent.DeletePushRule("global", pushrules.RoomRule, string(portal.MXID))
	} else {
		user.log.Debugfln("Muting portal %s", portal.MXID)
		err = intent.PutPushRule("global", pushrules.RoomRule, string(portal.MXID), &mautrix.ReqPutPushRule{
			Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		})
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.log.Warnfln("Failed to update push rule for %s through double puppet: %v", portal.MXID, err)
	}
}

func (user *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppetIntent := portal.bridge.GetPuppetByCustomMXID(user.MXID).CustomIntent()
	if doublePuppetIntent == nil || portal.MXID == "" {
		return
	}

	// TODO sync mute status properly
	if portal.GuildID != "" && user.bridge.Config.Bridge.MuteChannelsOnCreate {
		go user.mutePortal(doublePuppetIntent, portal, false)
	}
}

func (user *User) Login(token string) error {
	user.bridgeStateLock.Lock()
	user.wasLoggedOut = false
	user.bridgeStateLock.Unlock()
	user.DiscordToken = token
	user.Update()
	return user.Connect()
}

func (user *User) IsLoggedIn() bool {
	user.Lock()
	defer user.Unlock()

	return user.DiscordToken != ""
}

func (user *User) Logout() {
	user.Lock()
	defer user.Unlock()

	if user.DiscordID != "" {
		puppet := user.bridge.GetPuppetByID(user.DiscordID)
		if puppet.CustomMXID != "" {
			err := puppet.SwitchCustomMXID("", "")
			if err != nil {
				user.log.Warnln("Failed to logout-matrix while logging out of Discord:", err)
			}
		}
	}

	if user.Session != nil {
		if err := user.Session.Close(); err != nil {
			user.log.Warnln("Error closing session:", err)
		}
	}

	user.Session = nil
	user.DiscordID = ""
	user.DiscordToken = ""
	user.ReadStateVersion = 0
	user.Update()
}

func (user *User) Connected() bool {
	user.Lock()
	defer user.Unlock()

	return user.Session != nil
}

func (user *User) Connect() error {
	user.Lock()
	defer user.Unlock()

	if user.DiscordToken == "" {
		return ErrNotLoggedIn
	}

	user.log.Debugln("Connecting to discord")

	session, err := discordgo.New(user.DiscordToken)
	if err != nil {
		return err
	}
	// TODO move to config
	if os.Getenv("DISCORD_DEBUG") == "1" {
		session.LogLevel = discordgo.LogDebug
	}

	user.Session = session

	user.Session.AddHandler(user.readyHandler)
	user.Session.AddHandler(user.connectedHandler)
	user.Session.AddHandler(user.disconnectedHandler)
	user.Session.AddHandler(user.invalidAuthHandler)

	user.Session.AddHandler(user.guildCreateHandler)
	user.Session.AddHandler(user.guildDeleteHandler)
	user.Session.AddHandler(user.guildUpdateHandler)
	user.Session.AddHandler(user.guildRoleCreateHandler)
	user.Session.AddHandler(user.guildRoleUpdateHandler)
	user.Session.AddHandler(user.guildRoleDeleteHandler)

	user.Session.AddHandler(user.channelCreateHandler)
	user.Session.AddHandler(user.channelDeleteHandler)
	user.Session.AddHandler(user.channelPinsUpdateHandler)
	user.Session.AddHandler(user.channelUpdateHandler)

	user.Session.AddHandler(user.messageCreateHandler)
	user.Session.AddHandler(user.messageDeleteHandler)
	user.Session.AddHandler(user.messageUpdateHandler)
	user.Session.AddHandler(user.reactionAddHandler)
	user.Session.AddHandler(user.reactionRemoveHandler)
	user.Session.AddHandler(user.messageAckHandler)
	user.Session.AddHandler(user.typingStartHandler)

	user.Session.Identify.Presence.Status = "online"

	return user.Session.Open()
}

func (user *User) Disconnect() error {
	user.Lock()
	defer user.Unlock()
	if user.Session == nil {
		return ErrNotConnected
	}

	if err := user.Session.Close(); err != nil {
		return err
	}
	user.Session = nil
	return nil
}

func (user *User) bridgeMessage(guildID string) bool {
	if guildID == "" {
		return true
	}
	guild := user.bridge.GetGuildByID(guildID, false)
	return guild != nil && guild.MXID != ""
}

func (user *User) readyHandler(_ *discordgo.Session, r *discordgo.Ready) {
	user.log.Debugln("Discord connection ready")
	user.bridgeStateLock.Lock()
	user.wasLoggedOut = false
	user.bridgeStateLock.Unlock()

	if user.DiscordID != r.User.ID {
		user.DiscordID = r.User.ID
		user.Update()
	}
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateBackfilling})
	user.tryAutomaticDoublePuppeting()

	updateTS := time.Now()
	portalsInSpace := make(map[string]bool)
	for _, guild := range user.GetPortals() {
		portalsInSpace[guild.DiscordID] = guild.InSpace
	}
	for _, guild := range r.Guilds {
		user.handleGuild(guild, updateTS, portalsInSpace[guild.ID])
	}
	for i, ch := range r.PrivateChannels {
		portal := user.GetPortalByMeta(ch)
		user.handlePrivateChannel(portal, ch, updateTS, i < user.bridge.Config.Bridge.PrivateChannelCreateLimit, portalsInSpace[portal.Key.ChannelID])
	}
	user.PrunePortalList(updateTS)

	if r.ReadState.Version > user.ReadStateVersion {
		// TODO can we figure out which read states are actually new?
		for _, entry := range r.ReadState.Entries {
			user.messageAckHandler(nil, &discordgo.MessageAck{
				MessageID: string(entry.LastMessageID),
				ChannelID: entry.ID,
			})
		}
		user.ReadStateVersion = r.ReadState.Version
		user.Update()
	}

	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

func (user *User) addPrivateChannelToSpace(portal *Portal) bool {
	if portal.MXID == "" {
		return false
	}
	_, err := user.bridge.Bot.SendStateEvent(user.GetDMSpaceRoom(), event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{user.bridge.AS.HomeserverDomain},
	})
	if err != nil {
		user.log.Errorfln("Failed to add DM room %s to user DM space: %v", portal.MXID, err)
		return false
	} else {
		return true
	}
}

func (user *User) handlePrivateChannel(portal *Portal, meta *discordgo.Channel, timestamp time.Time, create, isInSpace bool) {
	if create && portal.MXID == "" {
		err := portal.CreateMatrixRoom(user, meta)
		if err != nil {
			user.log.Errorfln("Failed to create portal for private channel %s in initial sync: %v", portal.Key.ChannelID, err)
		}
	} else {
		portal.UpdateInfo(user, meta)
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
			user.log.Errorfln("Failed to add guild space %s to user space: %v", guild.MXID, err)
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

func (user *User) discordRoleToDB(guildID string, role *discordgo.Role, dbRole *database.Role) (*database.Role, bool) {
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
	return dbRole, changed
}

func (user *User) handleGuildRoles(guildID string, newRoles []*discordgo.Role) {
	existingRoles := user.bridge.DB.Role.GetAll(guildID)
	existingRoleMap := make(map[string]*database.Role, len(existingRoles))
	for _, role := range existingRoles {
		existingRoleMap[role.ID] = role
	}
	txn, err := user.bridge.DB.Begin()
	if err != nil {
		user.log.Errorln("Failed to start transaction for guild role sync:", err)
		panic(err)
	}
	for _, role := range newRoles {
		dbRole, changed := user.discordRoleToDB(guildID, role, existingRoleMap[role.ID])
		delete(existingRoleMap, role.ID)
		if changed {
			dbRole.Upsert(txn)
		}
	}
	for _, removeRole := range existingRoleMap {
		removeRole.Delete(txn)
	}
	err = txn.Commit()
	if err != nil {
		user.log.Errorln("Failed to commit guild role sync:", err)
		rollbackErr := txn.Rollback()
		if rollbackErr != nil {
			user.log.Errorln("Failed to rollback errored guild role sync:", rollbackErr)
		}
		panic(err)
	}
}

func (user *User) guildRoleCreateHandler(_ *discordgo.Session, r *discordgo.GuildRoleCreate) {
	dbRole, _ := user.discordRoleToDB(r.GuildID, r.Role, nil)
	dbRole.Upsert(nil)
}

func (user *User) guildRoleUpdateHandler(_ *discordgo.Session, r *discordgo.GuildRoleUpdate) {
	dbRole, _ := user.discordRoleToDB(r.GuildID, r.Role, nil)
	dbRole.Upsert(nil)
}

func (user *User) guildRoleDeleteHandler(_ *discordgo.Session, r *discordgo.GuildRoleDelete) {
	user.bridge.DB.Role.DeleteByID(r.GuildID, r.RoleID)
}

func (user *User) handleGuild(meta *discordgo.Guild, timestamp time.Time, isInSpace bool) {
	guild := user.bridge.GetGuildByID(meta.ID, true)
	guild.UpdateInfo(user, meta)
	if len(meta.Channels) > 0 {
		for _, ch := range meta.Channels {
			portal := user.GetPortalByMeta(ch)
			if guild.AutoBridgeChannels && portal.MXID == "" && user.channelIsBridgeable(ch) {
				err := portal.CreateMatrixRoom(user, ch)
				if err != nil {
					user.log.Errorfln("Failed to create portal for guild channel %s/%s in initial sync: %v", guild.ID, ch.ID, err)
				}
			} else {
				portal.UpdateInfo(user, ch)
			}
		}
	}
	if len(meta.Roles) > 0 {
		user.handleGuildRoles(meta.ID, meta.Roles)
	}
	user.addGuildToSpace(guild, isInSpace, timestamp)
}

func (user *User) connectedHandler(_ *discordgo.Session, _ *discordgo.Connect) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	user.log.Debugln("Connected to Discord")
	if user.wasDisconnected {
		user.wasDisconnected = false
		user.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	}
}

func (user *User) disconnectedHandler(_ *discordgo.Session, _ *discordgo.Disconnect) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	if user.wasLoggedOut {
		user.log.Debugln("Disconnected from Discord (not updating bridge state as user was just logged out)")
		return
	}
	user.log.Debugln("Disconnected from Discord")
	user.wasDisconnected = true
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "dc-transient-disconnect", Message: "Temporarily disconnected from Discord, trying to reconnect"})
}

func (user *User) invalidAuthHandler(_ *discordgo.Session, _ *discordgo.InvalidAuth) {
	user.bridgeStateLock.Lock()
	defer user.bridgeStateLock.Unlock()
	user.log.Debugln("Got logged out from Discord")
	user.wasLoggedOut = true
	user.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Error: "dc-websocket-disconnect-4004", Message: "Discord access token is no longer valid, please log in again"})
	go user.Logout()
}

func (user *User) guildCreateHandler(_ *discordgo.Session, g *discordgo.GuildCreate) {
	user.log.Infoln("Got guild create event for", g.ID)
	user.handleGuild(g.Guild, time.Now(), false)
}

func (user *User) guildDeleteHandler(_ *discordgo.Session, g *discordgo.GuildDelete) {
	user.log.Infoln("Got guild delete event for", g.ID)
	user.MarkNotInPortal(g.ID)
	guild := user.bridge.GetGuildByID(g.ID, false)
	if guild == nil || guild.MXID == "" {
		return
	}
	if user.bridge.Config.Bridge.DeleteGuildOnLeave && !user.PortalHasOtherUsers(g.ID) {
		user.log.Debugfln("No other users in %s, cleaning up all portals", g.ID)
		err := user.unbridgeGuild(g.ID)
		if err != nil {
			user.log.Warnfln("Failed to unbridge guild that was deleted: %v", err)
		}
	}
}

func (user *User) guildUpdateHandler(_ *discordgo.Session, g *discordgo.GuildUpdate) {
	user.log.Debugln("Got guild update event for", g.ID)
	user.handleGuild(g.Guild, time.Now(), user.IsInSpace(g.ID))
}

func (user *User) channelCreateHandler(_ *discordgo.Session, c *discordgo.ChannelCreate) {
	if !user.bridgeMessage(c.GuildID) {
		user.log.Debugfln("Ignoring channel create event in unbridged guild %s/%s", c.GuildID, c.ID)
		return
	}
	user.log.Infofln("Got channel create event for %s/%s", c.GuildID, c.ID)
	portal := user.GetPortalByMeta(c.Channel)
	if portal.MXID != "" {
		return
	}
	if c.GuildID == "" {
		user.handlePrivateChannel(portal, c.Channel, time.Now(), true, user.IsInSpace(portal.Key.String()))
	} else if user.channelIsBridgeable(c.Channel) {
		err := portal.CreateMatrixRoom(user, c.Channel)
		if err != nil {
			user.log.Errorfln("Error creating Matrix room for %s on channel create event: %v", c.ID, err)
		}
	} else {
		user.log.Debugfln("Got channel create event for %s, but it's not bridgeable, ignoring", c.ID)
	}
}

func (user *User) channelDeleteHandler(_ *discordgo.Session, c *discordgo.ChannelDelete) {
	portal := user.GetExistingPortalByID(c.ID)
	if portal == nil {
		user.log.Debugfln("Ignoring delete of unknown channel %s/%s", c.GuildID, c.ID)
		return
	}
	user.log.Infofln("Got channel delete event for %s/%s, cleaning up portal", c.GuildID, c.ID)
	portal.Delete()
	portal.cleanup(!user.bridge.Config.Bridge.DeletePortalOnChannelDelete)
	if c.GuildID == "" {
		user.MarkNotInPortal(portal.Key.ChannelID)
	}
	user.log.Debugfln("Completed cleaning up %s/%s", c.GuildID, c.ID)
}

func (user *User) channelPinsUpdateHandler(_ *discordgo.Session, c *discordgo.ChannelPinsUpdate) {
	user.log.Debugln("channel pins update")
}

func (user *User) channelUpdateHandler(_ *discordgo.Session, c *discordgo.ChannelUpdate) {
	portal := user.GetPortalByMeta(c.Channel)
	if c.GuildID == "" {
		user.handlePrivateChannel(portal, c.Channel, time.Now(), true, user.IsInSpace(portal.Key.String()))
	} else {
		portal.UpdateInfo(user, c.Channel)
	}
}

func (user *User) pushPortalMessage(msg interface{}, typeName, channelID, guildID string) {
	if !user.bridgeMessage(guildID) {
		return
	}

	portal := user.GetExistingPortalByID(channelID)
	var thread *Thread
	if portal == nil {
		thread = user.bridge.GetThreadByID(channelID, nil)
		if thread == nil || thread.Parent == nil {
			user.log.Debugfln("Dropping %s in unknown channel %s/%s", typeName, guildID, channelID)
			return
		}
		portal = thread.Parent
	}

	portal.discordMessages <- portalDiscordMessage{
		msg:    msg,
		user:   user,
		thread: thread,
	}
}

func (user *User) messageCreateHandler(_ *discordgo.Session, m *discordgo.MessageCreate) {
	user.pushPortalMessage(m, "message create", m.ChannelID, m.GuildID)
}

func (user *User) messageDeleteHandler(_ *discordgo.Session, m *discordgo.MessageDelete) {
	user.pushPortalMessage(m, "message delete", m.ChannelID, m.GuildID)
}

func (user *User) messageUpdateHandler(_ *discordgo.Session, m *discordgo.MessageUpdate) {
	user.pushPortalMessage(m, "message update", m.ChannelID, m.GuildID)
}

func (user *User) reactionAddHandler(_ *discordgo.Session, m *discordgo.MessageReactionAdd) {
	user.pushPortalMessage(m, "reaction add", m.ChannelID, m.GuildID)
}

func (user *User) reactionRemoveHandler(_ *discordgo.Session, m *discordgo.MessageReactionRemove) {
	user.pushPortalMessage(m, "reaction remove", m.ChannelID, m.GuildID)
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

func (user *User) messageAckHandler(_ *discordgo.Session, m *discordgo.MessageAck) {
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
		user.log.Debugfln("Dropping message ack event for unknown message %s/%s", m.ChannelID, m.MessageID)
		return
	}
	err := dp.CustomIntent().SetReadMarkers(portal.MXID, user.makeReadMarkerContent(msg.MXID))
	if err != nil {
		user.log.Warnfln("Failed to mark %s/%s as read: %v", msg.MXID, msg.DiscordID, err)
	} else {
		user.log.Debugfln("Marked %s/%s as read after Discord message ack event", msg.MXID, msg.DiscordID)
		if user.ReadStateVersion < m.Version {
			user.ReadStateVersion = m.Version
			// TODO maybe don't update every time?
			user.Update()
		}
	}
}

func (user *User) typingStartHandler(_ *discordgo.Session, t *discordgo.TypingStart) {
	portal := user.GetExistingPortalByID(t.ChannelID)
	if portal == nil || portal.MXID == "" {
		return
	}
	puppet := user.bridge.GetPuppetByID(t.UserID)
	_, err := puppet.IntentFor(portal).UserTyping(portal.MXID, true, 12*time.Second)
	if err != nil {
		user.log.Warnfln("Failed to mark %s as typing in %s: %v", puppet.MXID, portal.MXID, err)
	}
}

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) bool {
	if intent == nil {
		intent = user.bridge.Bot
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
		user.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ret = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
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

	user.log.Debugln("Updating m.direct list on homeserver")

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
			user.log.Warnln("Failed to get m.direct list to update it:", err)

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
		user.log.Warnln("Failed to update m.direct list:", err)
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
	user.addGuildToSpace(guild, false, time.Now())
	for _, ch := range meta.Channels {
		portal := user.GetPortalByMeta(ch)
		if (everything && user.channelIsBridgeable(ch)) || ch.Type == discordgo.ChannelTypeGuildCategory {
			err = portal.CreateMatrixRoom(user, ch)
			if err != nil {
				user.log.Warnfln("Error creating room for guild channel %s: %v", ch.ID, err)
			}
		}
	}
	guild.AutoBridgeChannels = everything
	guild.Update()

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
	if !guild.AutoBridgeChannels && guild.MXID == "" {
		return errors.New("that guild is not bridged")
	}
	guild.AutoBridgeChannels = false
	guild.Update()
	for _, portal := range user.bridge.GetAllPortalsInGuild(guild.ID) {
		portal.cleanup(false)
		portal.RemoveMXID()
	}
	guild.cleanup()
	guild.RemoveMXID()
	return nil
}
