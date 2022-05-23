package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	log "maunium.net/go/maulogger/v2"

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

	guilds     map[string]*database.Guild
	guildsLock sync.Mutex

	Session *discordgo.Session
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
	if user.ID == "" {
		return nil
	}
	p := user.bridge.GetPuppetByID(user.ID)
	if p == nil {
		return nil
	}
	return p
}

var _ bridge.User = (*User)(nil)

// this assume you are holding the guilds lock!!!
func (user *User) loadGuilds() {
	user.guilds = map[string]*database.Guild{}
	for _, guild := range user.bridge.DB.Guild.GetAll(user.ID) {
		user.guilds[guild.GuildID] = guild
	}
}

func (br *DiscordBridge) loadUser(dbUser *database.User, mxid *id.UserID) *User {
	// If we weren't passed in a user we attempt to create one if we were given
	// a matrix id.
	if dbUser == nil {
		if mxid == nil {
			return nil
		}

		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}

	user := br.NewUser(dbUser)

	// We assume the usersLock was acquired by our caller.
	br.usersByMXID[user.MXID] = user
	if user.ID != "" {
		br.usersByID[user.ID] = user
	}

	if user.ManagementRoom != "" {
		// Lock the management rooms for our update
		br.managementRoomsLock.Lock()
		br.managementRooms[user.ManagementRoom] = user
		br.managementRoomsLock.Unlock()
	}

	// Load our guilds state from the database and turn it into a map
	user.guildsLock.Lock()
	user.loadGuilds()
	user.guildsLock.Unlock()

	return user
}

func (br *DiscordBridge) GetUserByMXID(userID id.UserID) *User {
	// TODO: check if puppet

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
		guilds: map[string]*database.Guild{},
	}

	user.PermissionLevel = br.Config.Bridge.Permissions.Get(user.MXID)

	return user
}

func (br *DiscordBridge) getAllUsers() []*User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	dbUsers := br.DB.User.GetAll()
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

	for _, u := range br.getAllUsers() {
		go func(user *User) {
			err := user.Connect()
			if err != nil {
				user.log.Errorfln("Error connecting: %v", err)
			}
		}(u)
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
		// If there's a user already assigned to this management room, clear it
		// out.
		// I think this is due a name change or something? I dunno, leaving it
		// for now.
		existing.ManagementRoom = ""
		existing.Update()
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	user.Update()
}

func (user *User) tryAutomaticDoublePuppeting() {
	user.Lock()
	defer user.Unlock()

	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}

	user.log.Debugln("Checking if double puppeting needs to be enabled")

	puppet := user.bridge.GetPuppetByID(user.ID)
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
	user.Token = token
	user.Update()
	return user.Connect()
}

func (user *User) IsLoggedIn() bool {
	user.Lock()
	defer user.Unlock()

	return user.Token != ""
}

func (user *User) Logout() error {
	user.Lock()
	defer user.Unlock()

	if user.Session == nil {
		return ErrNotLoggedIn
	}

	puppet := user.bridge.GetPuppetByID(user.ID)
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

	user.Token = ""
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

	if user.Token == "" {
		return ErrNotLoggedIn
	}

	user.log.Debugln("connecting to discord")

	session, err := discordgo.New(user.Token)
	if err != nil {
		return err
	}

	user.Session = session

	// Add our event handlers
	user.Session.AddHandler(user.readyHandler)
	user.Session.AddHandler(user.connectedHandler)
	user.Session.AddHandler(user.disconnectedHandler)

	user.Session.AddHandler(user.guildCreateHandler)
	user.Session.AddHandler(user.guildDeleteHandler)
	user.Session.AddHandler(user.guildUpdateHandler)

	user.Session.AddHandler(user.channelCreateHandler)
	user.Session.AddHandler(user.channelDeleteHandler)
	user.Session.AddHandler(user.channelPinsUpdateHandler)
	user.Session.AddHandler(user.channelUpdateHandler)

	user.Session.AddHandler(user.messageCreateHandler)
	user.Session.AddHandler(user.messageDeleteHandler)
	user.Session.AddHandler(user.messageUpdateHandler)
	user.Session.AddHandler(user.reactionAddHandler)
	user.Session.AddHandler(user.reactionRemoveHandler)

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
	// Non guild message always get bridged.
	if guildID == "" {
		return true
	}

	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	if guild, found := user.guilds[guildID]; found {
		if guild.Bridge {
			return true
		}
	}

	user.log.Debugfln("ignoring message for non-bridged guild %s-%s", user.ID, guildID)

	return false
}

func (user *User) readyHandler(s *discordgo.Session, r *discordgo.Ready) {
	user.log.Debugln("discord connection ready")

	// Update our user fields
	user.ID = r.User.ID

	// Update our guild map to match watch discord thinks we're in. This is the
	// only time we can get the full guild map as discordgo doesn't make it
	// available to us later. Also, discord might not give us the full guild
	// information here, so we use this to remove guilds the user left and only
	// add guilds whose full information we have. The are told about the
	// "unavailable" guilds later via the GuildCreate handler.
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	// build a list of the current guilds we're in so we can prune the old ones
	current := []string{}

	user.log.Debugln("database guild count", len(user.guilds))
	user.log.Debugln("discord guild count", len(r.Guilds))

	for _, guild := range r.Guilds {
		current = append(current, guild.ID)

		// If we already know about this guild, make sure we reset it's bridge
		// status.
		if val, found := user.guilds[guild.ID]; found {
			bridge := val.Bridge
			user.guilds[guild.ID].Bridge = bridge

			// Update the name if the guild is available
			if !guild.Unavailable {
				user.guilds[guild.ID].GuildName = guild.Name
			}

			val.Upsert()
		} else {
			g := user.bridge.DB.Guild.New()
			g.DiscordID = user.ID
			g.GuildID = guild.ID
			user.guilds[guild.ID] = g

			if !guild.Unavailable {
				g.GuildName = guild.Name
			}

			g.Upsert()
		}
	}

	// Sync the guilds to the database.
	user.bridge.DB.Guild.Prune(user.ID, current)

	// Finally reload from the database since it purged servers we're not in
	// anymore.
	user.loadGuilds()

	user.log.Debugln("updated database guild count", len(user.guilds))

	user.Update()
}

func (user *User) connectedHandler(s *discordgo.Session, c *discordgo.Connect) {
	user.log.Debugln("connected to discord")

	user.tryAutomaticDoublePuppeting()
}

func (user *User) disconnectedHandler(s *discordgo.Session, d *discordgo.Disconnect) {
	user.log.Debugln("disconnected from discord")
}

func (user *User) guildCreateHandler(s *discordgo.Session, g *discordgo.GuildCreate) {
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	// If we somehow already know about the guild, just update it's name
	if guild, found := user.guilds[g.ID]; found {
		guild.GuildName = g.Name
		guild.Upsert()

		return
	}

	// This is a brand new guild so lets get it added.
	guild := user.bridge.DB.Guild.New()
	guild.DiscordID = user.ID
	guild.GuildID = g.ID
	guild.GuildName = g.Name
	guild.Upsert()

	user.guilds[g.ID] = guild
}

func (user *User) guildDeleteHandler(s *discordgo.Session, g *discordgo.GuildDelete) {
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	if guild, found := user.guilds[g.ID]; found {
		guild.Delete()
		delete(user.guilds, g.ID)
		user.log.Debugln("deleted guild", g.Guild.ID)
	}
}

func (user *User) guildUpdateHandler(s *discordgo.Session, g *discordgo.GuildUpdate) {
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	// If we somehow already know about the guild, just update it's name
	if guild, found := user.guilds[g.ID]; found {
		guild.GuildName = g.Name
		guild.Upsert()

		user.log.Debugln("updated guild", g.ID)
	}
}

func (user *User) createChannel(c *discordgo.Channel) {
	key := database.NewPortalKey(c.ID, user.User.ID)
	portal := user.bridge.GetPortalByID(key)

	if portal.MXID != "" {
		return
	}

	portal.Name = c.Name
	portal.Topic = c.Topic
	portal.Type = c.Type

	if portal.Type == discordgo.ChannelTypeDM {
		portal.DMUser = c.Recipients[0].ID
	}

	if c.Icon != "" {
		user.log.Debugln("channel icon", c.Icon)
	}

	portal.Update()

	portal.createMatrixRoom(user, c)
}

func (user *User) channelCreateHandler(s *discordgo.Session, c *discordgo.ChannelCreate) {
	user.createChannel(c.Channel)
}

func (user *User) channelDeleteHandler(s *discordgo.Session, c *discordgo.ChannelDelete) {
	user.log.Debugln("channel delete handler")
}

func (user *User) channelPinsUpdateHandler(s *discordgo.Session, c *discordgo.ChannelPinsUpdate) {
	user.log.Debugln("channel pins update")
}

func (user *User) channelUpdateHandler(s *discordgo.Session, c *discordgo.ChannelUpdate) {
	key := database.NewPortalKey(c.ID, user.User.ID)
	portal := user.bridge.GetPortalByID(key)

	portal.update(user, c.Channel)
}

func (user *User) messageCreateHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !user.bridgeMessage(m.GuildID) {
		return
	}

	key := database.NewPortalKey(m.ChannelID, user.ID)
	portal := user.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: user,
	}

	portal.discordMessages <- msg
}

func (user *User) messageDeleteHandler(s *discordgo.Session, m *discordgo.MessageDelete) {
	if !user.bridgeMessage(m.GuildID) {
		return
	}

	key := database.NewPortalKey(m.ChannelID, user.ID)
	portal := user.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: user,
	}

	portal.discordMessages <- msg
}

func (user *User) messageUpdateHandler(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if !user.bridgeMessage(m.GuildID) {
		return
	}

	key := database.NewPortalKey(m.ChannelID, user.ID)
	portal := user.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: user,
	}

	portal.discordMessages <- msg
}

func (user *User) reactionAddHandler(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
	if !user.bridgeMessage(m.MessageReaction.GuildID) {
		return
	}

	key := database.NewPortalKey(m.ChannelID, user.User.ID)
	portal := user.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: user,
	}

	portal.discordMessages <- msg
}

func (user *User) reactionRemoveHandler(s *discordgo.Session, m *discordgo.MessageReactionRemove) {
	if !user.bridgeMessage(m.MessageReaction.GuildID) {
		return
	}

	key := database.NewPortalKey(m.ChannelID, user.User.ID)
	portal := user.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: user,
	}

	portal.discordMessages <- msg
}

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) bool {
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

	privateChats := user.bridge.DB.Portal.FindPrivateChats(user.ID)
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
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	guild, found := user.guilds[guildID]
	if !found {
		return fmt.Errorf("guildID not found")
	}

	// Update the guild
	guild.Bridge = true
	guild.Upsert()

	// If this is a full bridge, create portals for all the channels
	if everything {
		channels, err := user.Session.GuildChannels(guildID)
		if err != nil {
			return err
		}

		for _, channel := range channels {
			if channelIsBridgeable(channel) {
				user.createChannel(channel)
			}
		}
	}

	return nil
}

func (user *User) unbridgeGuild(guildID string) error {
	user.guildsLock.Lock()
	defer user.guildsLock.Unlock()

	guild, exists := user.guilds[guildID]
	if !exists {
		return fmt.Errorf("guildID not found")
	}

	if !guild.Bridge {
		return fmt.Errorf("guild not bridged")
	}

	// First update the guild so we don't have any other go routines recreating
	// channels we're about to destroy.
	guild.Bridge = false
	guild.Upsert()

	// Now run through the channels in the guild and remove any portals we
	// have for them.
	channels, err := user.Session.GuildChannels(guildID)
	if err != nil {
		return err
	}

	for _, channel := range channels {
		if channelIsBridgeable(channel) {
			key := database.PortalKey{
				ChannelID: channel.ID,
				Receiver:  user.ID,
			}

			portal := user.bridge.GetPortalByID(key)
			portal.leave(user)
		}
	}

	return nil
}
