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
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

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

	BridgeState *bridge.BridgeStateQueue

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
		go func(user *User) {
			user.BridgeState.Send(bridge.State{StateEvent: bridge.StateConnecting})
			err := user.Connect()
			if err != nil {
				user.log.Errorfln("Error connecting: %v", err)
				if closeErr := (&websocket.CloseError{}); errors.As(err, &closeErr) && closeErr.Code == 4004 {
					user.BridgeState.Send(bridge.State{StateEvent: bridge.StateBadCredentials, Message: err.Error()})
					user.DiscordToken = ""
					user.Update()
				} else {
					user.BridgeState.Send(bridge.State{StateEvent: bridge.StateUnknownError, Message: err.Error()})
				}
			}
		}(u)
	}
	if len(usersWithToken) == 0 {
		br.SendGlobalBridgeState(bridge.State{StateEvent: bridge.StateUnconfigured}.Fill(nil))
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

func (user *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if doublePuppet == nil {
		return
	}

	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || portal.MXID == "" {
		return
	}

	// TODO sync mute status
}

func (user *User) Login(token string) error {
	user.DiscordToken = token
	user.Update()
	return user.Connect()
}

func (user *User) IsLoggedIn() bool {
	user.Lock()
	defer user.Unlock()

	return user.DiscordToken != ""
}

func (user *User) Logout() error {
	user.Lock()
	defer user.Unlock()

	if user.Session == nil {
		return ErrNotLoggedIn
	}

	puppet := user.bridge.GetPuppetByID(user.DiscordID)
	if puppet.CustomMXID != "" {
		err := puppet.SwitchCustomMXID("", "")
		if err != nil {
			user.log.Warnln("Failed to logout-matrix while logging out of Discord:", err)
		}
	}

	if err := user.Session.Close(); err != nil {
		return err
	}

	user.Session = nil

	user.DiscordToken = ""
	user.Update()

	return nil
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

	if user.DiscordID != r.User.ID {
		user.DiscordID = r.User.ID
		user.Update()
	}
	user.BridgeState.Send(bridge.State{StateEvent: bridge.StateBackfilling})

	updateTS := time.Now()
	portalsInSpace := make(map[string]bool)
	for _, guild := range user.GetPortals() {
		portalsInSpace[guild.DiscordID] = guild.InSpace
	}
	for _, guild := range r.Guilds {
		user.handleGuild(guild, updateTS, portalsInSpace[guild.ID])
	}
	user.PrunePortalList(updateTS)
	for i, ch := range r.PrivateChannels {
		portal := user.GetPortalByMeta(ch)
		user.handlePrivateChannel(portal, ch, updateTS, i < user.bridge.Config.Bridge.PrivateChannelCreateLimit, portalsInSpace[portal.Key.ChannelID])
	}

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

	user.BridgeState.Send(bridge.State{StateEvent: bridge.StateConnected})
}

func (user *User) handlePrivateChannel(portal *Portal, meta *discordgo.Channel, timestamp time.Time, create, isInSpace bool) {
	if create && portal.MXID == "" {
		err := portal.CreateMatrixRoom(user, meta)
		if err != nil {
			user.log.Errorfln("Failed to create portal for private channel %s in initial sync: %v", meta.ID, err)
		}
	} else {
		portal.UpdateInfo(user, meta)
	}
	if len(portal.MXID) > 0 && !isInSpace {
		_, err := user.bridge.Bot.SendStateEvent(user.GetDMSpaceRoom(), event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
			Via: []string{user.bridge.AS.HomeserverDomain},
		})
		if err != nil {
			user.log.Errorfln("Failed to add DM room %s to user DM space: %v", portal.MXID, err)
		} else {
			isInSpace = true
		}
	}
	user.MarkInPortal(database.UserPortal{
		DiscordID: meta.ID,
		Type:      database.UserPortalTypeDM,
		Timestamp: timestamp,
		InSpace:   isInSpace,
	})
}

func (user *User) addGuildToSpace(guild *Guild) bool {
	if len(guild.MXID) > 0 {
		_, err := user.bridge.Bot.SendStateEvent(user.GetSpaceRoom(), event.StateSpaceChild, guild.MXID.String(), &event.SpaceChildEventContent{
			Via: []string{user.bridge.AS.HomeserverDomain},
		})
		if err != nil {
			user.log.Errorfln("Failed to add guild space %s to user space: %v", guild.MXID, err)
		} else {
			return true
		}
	}
	return false
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
		return
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
			if (guild.AutoBridgeChannels && channelIsBridgeable(ch)) && portal.MXID == "" {
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
	if !isInSpace {
		isInSpace = user.addGuildToSpace(guild)
	}
	user.MarkInPortal(database.UserPortal{
		DiscordID: meta.ID,
		Type:      database.UserPortalTypeGuild,
		Timestamp: timestamp,
		InSpace:   isInSpace,
	})
}

func (user *User) connectedHandler(_ *discordgo.Session, c *discordgo.Connect) {
	user.log.Debugln("Connected to discord")

	user.tryAutomaticDoublePuppeting()
	if user.BridgeState.GetPrev().StateEvent == bridge.StateTransientDisconnect {
		user.BridgeState.Send(bridge.State{StateEvent: bridge.StateConnected})
	}
}

func (user *User) disconnectedHandler(_ *discordgo.Session, d *discordgo.Disconnect) {
	user.log.Debugln("Disconnected from discord")
	user.BridgeState.Send(bridge.State{StateEvent: bridge.StateTransientDisconnect})
}

func (user *User) guildCreateHandler(_ *discordgo.Session, g *discordgo.GuildCreate) {
	user.handleGuild(g.Guild, time.Now(), false)
}

func (user *User) guildDeleteHandler(_ *discordgo.Session, g *discordgo.GuildDelete) {
	user.MarkNotInPortal(g.ID)
	guild := user.bridge.GetGuildByID(g.ID, false)
	if guild == nil || guild.MXID == "" {
		return
	}
	// TODO clean up?
}

func (user *User) guildUpdateHandler(_ *discordgo.Session, g *discordgo.GuildUpdate) {
	user.handleGuild(g.Guild, time.Now(), user.IsInSpace(g.ID))
}

func (user *User) channelCreateHandler(_ *discordgo.Session, c *discordgo.ChannelCreate) {
	if !user.bridgeMessage(c.GuildID) {
		return
	}
	portal := user.GetPortalByMeta(c.Channel)
	if portal.MXID != "" {
		return
	}
	if c.GuildID == "" {
		user.handlePrivateChannel(portal, c.Channel, time.Now(), true, user.IsInSpace(portal.Key.String()))
	} else {
		err := portal.CreateMatrixRoom(user, c.Channel)
		if err != nil {
			user.log.Errorfln("Error creating Matrix room for %s on channel create event: %v", c.ID, err)
		}
	}
}

func (user *User) channelDeleteHandler(_ *discordgo.Session, c *discordgo.ChannelDelete) {
	user.log.Debugln("channel delete handler")
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
	if user.bridge.Config.Homeserver.Asmux {
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
	guild.AutoBridgeChannels = everything
	meta, _ := user.Session.State.Guild(guildID)
	err := guild.CreateMatrixRoom(user, meta)
	if err != nil {
		return err
	}
	user.addGuildToSpace(guild)
	user.MarkInPortal(database.UserPortal{
		DiscordID: meta.ID,
		Type:      database.UserPortalTypeGuild,
		Timestamp: time.Now(),
		InSpace:   true,
	})
	for _, ch := range meta.Channels {
		portal := user.GetPortalByMeta(ch)
		if (everything && channelIsBridgeable(ch)) || ch.Type == discordgo.ChannelTypeGuildCategory {
			err = portal.CreateMatrixRoom(user, ch)
			if err != nil {
				user.log.Warnfln("Error creating room for guild channel %s: %v", ch.ID, err)
			}
		}
	}

	return nil
}

func (user *User) unbridgeGuild(guildID string) error {
	//user.guildsLock.Lock()
	//defer user.guildsLock.Unlock()
	//
	//guild, exists := user.guilds[guildID]
	//if !exists {
	//	return fmt.Errorf("guildID not found")
	//}
	//
	//if !guild.Bridge {
	//	return fmt.Errorf("guild not bridged")
	//}
	//
	//// First update the guild so we don't have any other go routines recreating
	//// channels we're about to destroy.
	//guild.Bridge = false
	//guild.Upsert()
	//
	//// Now run through the channels in the guild and remove any portals we
	//// have for them.
	//channels, err := user.Session.GuildChannels(guildID)
	//if err != nil {
	//	return err
	//}
	//
	//for _, channel := range channels {
	//	if channelIsBridgeable(channel) {
	//		key := database.PortalKey{
	//			ChannelID: channel.ID,
	//			Receiver:  user.DiscordID,
	//		}
	//
	//		portal := user.bridge.GetPortalByID(key)
	//		portal.leave(user)
	//	}
	//}

	return errors.New("unbridging is not currently supported")
}
