package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/skip2/go-qrcode"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/database"
)

var (
	ErrNotConnected = errors.New("not connected")
	ErrNotLoggedIn  = errors.New("not logged in")
)

type User struct {
	*database.User

	sync.Mutex

	bridge *Bridge
	log    log.Logger

	Session *discordgo.Session
}

func (b *Bridge) loadUser(dbUser *database.User, mxid *id.UserID) *User {
	// If we weren't passed in a user we attempt to create one if we were given
	// a matrix id.
	if dbUser == nil {
		if mxid == nil {
			return nil
		}

		dbUser = b.db.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}

	user := b.NewUser(dbUser)

	// We assume the usersLock was acquired by our caller.
	b.usersByMXID[user.MXID] = user
	if user.ID != "" {
		b.usersByID[user.ID] = user
	}

	if user.ManagementRoom != "" {
		// Lock the management rooms for our update
		b.managementRoomsLock.Lock()
		b.managementRooms[user.ManagementRoom] = user
		b.managementRoomsLock.Unlock()
	}

	return user
}

func (b *Bridge) GetUserByMXID(userID id.UserID) *User {
	// TODO: check if puppet

	b.usersLock.Lock()
	defer b.usersLock.Unlock()

	user, ok := b.usersByMXID[userID]
	if !ok {
		return b.loadUser(b.db.User.GetByMXID(userID), &userID)
	}

	return user
}

func (b *Bridge) GetUserByID(id string) *User {
	b.usersLock.Lock()
	defer b.usersLock.Unlock()

	user, ok := b.usersByID[id]
	if !ok {
		return b.loadUser(b.db.User.GetByID(id), nil)
	}

	return user
}

func (b *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: b,
		log:    b.log.Sub("User").Sub(string(dbUser.MXID)),
	}

	return user
}

func (b *Bridge) getAllUsers() []*User {
	b.usersLock.Lock()
	defer b.usersLock.Unlock()

	dbUsers := b.db.User.GetAll()
	users := make([]*User, len(dbUsers))

	for idx, dbUser := range dbUsers {
		user, ok := b.usersByMXID[dbUser.MXID]
		if !ok {
			user = b.loadUser(dbUser, nil)
		}
		users[idx] = user
	}

	return users
}

func (b *Bridge) startUsers() {
	b.log.Debugln("Starting users")

	for _, user := range b.getAllUsers() {
		go user.Connect()
	}

	b.log.Debugln("Starting custom puppets")
	for _, customPuppet := range b.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			b.log.Debugln("Starting custom puppet", puppet.CustomMXID)

			if err := puppet.StartCustomMXID(true); err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
			}
		}(customPuppet)
	}
}

func (u *User) SetManagementRoom(roomID id.RoomID) {
	u.bridge.managementRoomsLock.Lock()
	defer u.bridge.managementRoomsLock.Unlock()

	existing, ok := u.bridge.managementRooms[roomID]
	if ok {
		// If there's a user already assigned to this management room, clear it
		// out.
		// I think this is due a name change or something? I dunno, leaving it
		// for now.
		existing.ManagementRoom = ""
		existing.Update()
	}

	u.ManagementRoom = roomID
	u.bridge.managementRooms[u.ManagementRoom] = u
	u.Update()
}

func (u *User) sendQRCode(bot *appservice.IntentAPI, roomID id.RoomID, code string) (id.EventID, error) {
	url, err := u.uploadQRCode(code)
	if err != nil {
		return "", err
	}

	content := event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    code,
		URL:     url.CUString(),
	}

	resp, err := bot.SendMessageEvent(roomID, event.EventMessage, &content)
	if err != nil {
		return "", err
	}

	return resp.EventID, nil
}

func (u *User) uploadQRCode(code string) (id.ContentURI, error) {
	qrCode, err := qrcode.Encode(code, qrcode.Low, 256)
	if err != nil {
		u.log.Errorln("Failed to encode QR code:", err)

		return id.ContentURI{}, err
	}

	bot := u.bridge.as.BotClient()

	resp, err := bot.UploadBytes(qrCode, "image/png")
	if err != nil {
		u.log.Errorln("Failed to upload QR code:", err)

		return id.ContentURI{}, err
	}

	return resp.ContentURI, nil
}

func (u *User) tryAutomaticDoublePuppeting() {
	u.Lock()
	defer u.Unlock()

	if !u.bridge.Config.CanAutoDoublePuppet(u.MXID) {
		return
	}

	u.log.Debugln("Checking if double puppeting needs to be enabled")

	puppet := u.bridge.GetPuppetByID(u.ID)
	if puppet.CustomMXID != "" {
		u.log.Debugln("User already has double-puppeting enabled")

		return
	}

	accessToken, err := puppet.loginWithSharedSecret(u.MXID)
	if err != nil {
		u.log.Warnln("Failed to login with shared secret:", err)

		return
	}

	err = puppet.SwitchCustomMXID(accessToken, u.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)

		return
	}

	u.log.Infoln("Successfully automatically enabled custom puppet")
}

func (u *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppet := portal.bridge.GetPuppetByCustomMXID(u.MXID)
	if doublePuppet == nil {
		return
	}

	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || portal.MXID == "" {
		return
	}

	// TODO sync mute status
}

func (u *User) Login(token string) error {
	if token == "" {
		return fmt.Errorf("No token specified")
	}

	u.Token = token
	u.Update()

	return u.Connect()
}

func (u *User) LoggedIn() bool {
	u.Lock()
	defer u.Unlock()

	return u.Token != ""
}

func (u *User) Logout() error {
	u.Lock()
	defer u.Unlock()

	if u.Session == nil {
		return ErrNotLoggedIn
	}

	puppet := u.bridge.GetPuppetByID(u.ID)
	if puppet.CustomMXID != "" {
		err := puppet.SwitchCustomMXID("", "")
		if err != nil {
			u.log.Warnln("Failed to logout-matrix while logging out of Discord:", err)
		}
	}

	if err := u.Session.Close(); err != nil {
		return err
	}

	u.Session = nil

	u.Token = ""
	u.Update()

	return nil
}

func (u *User) Connected() bool {
	u.Lock()
	defer u.Unlock()

	return u.Session != nil
}

func (u *User) Connect() error {
	u.Lock()
	defer u.Unlock()

	if u.Token == "" {
		return ErrNotLoggedIn
	}

	u.log.Debugln("connecting to discord")

	session, err := discordgo.New(u.Token)
	if err != nil {
		return err
	}

	u.Session = session

	// get our user info
	user, err := u.Session.User("@me")
	if err != nil {
		return err
	}

	u.User.ID = user.ID

	// Add our event handlers
	u.Session.AddHandler(u.connectedHandler)
	u.Session.AddHandler(u.disconnectedHandler)

	u.Session.AddHandler(u.channelCreateHandler)
	u.Session.AddHandler(u.channelDeleteHandler)
	u.Session.AddHandler(u.channelPinsUpdateHandler)
	u.Session.AddHandler(u.channelUpdateHandler)

	u.Session.AddHandler(u.messageCreateHandler)
	u.Session.AddHandler(u.messageDeleteHandler)
	u.Session.AddHandler(u.messageUpdateHandler)
	u.Session.AddHandler(u.reactionAddHandler)
	u.Session.AddHandler(u.reactionRemoveHandler)

	u.Session.Identify.Presence.Status = "online"

	return u.Session.Open()
}

func (u *User) Disconnect() error {
	u.Lock()
	defer u.Unlock()

	if u.Session == nil {
		return ErrNotConnected
	}

	if err := u.Session.Close(); err != nil {
		return err
	}

	u.Session = nil

	return nil
}

func (u *User) connectedHandler(s *discordgo.Session, c *discordgo.Connect) {
	u.log.Debugln("connected to discord")

	u.tryAutomaticDoublePuppeting()
}

func (u *User) disconnectedHandler(s *discordgo.Session, d *discordgo.Disconnect) {
	u.log.Debugln("disconnected from discord")
}

func (u *User) channelCreateHandler(s *discordgo.Session, c *discordgo.ChannelCreate) {
	key := database.NewPortalKey(c.ID, u.User.ID)
	portal := u.bridge.GetPortalByID(key)

	portal.Name = c.Name
	portal.Topic = c.Topic
	portal.Type = c.Type

	if portal.Type == discordgo.ChannelTypeDM {
		portal.DMUser = c.Recipients[0].ID
	}

	if c.Icon != "" {
		u.log.Debugln("channel icon", c.Icon)
	}

	portal.Update()

	portal.createMatrixRoom(u, c.Channel)
}

func (u *User) channelDeleteHandler(s *discordgo.Session, c *discordgo.ChannelDelete) {
	u.log.Debugln("channel delete handler")
}

func (u *User) channelPinsUpdateHandler(s *discordgo.Session, c *discordgo.ChannelPinsUpdate) {
	u.log.Debugln("channel pins update")
}

func (u *User) channelUpdateHandler(s *discordgo.Session, c *discordgo.ChannelUpdate) {
	key := database.NewPortalKey(c.ID, u.User.ID)
	portal := u.bridge.GetPortalByID(key)

	portal.Name = c.Name
	portal.Topic = c.Topic
	u.log.Debugln("channel icon", c.Icon)
	portal.Update()

	u.log.Debugln("channel update")
}

func (u *User) messageCreateHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring message for guild")

		return
	}

	key := database.NewPortalKey(m.ChannelID, u.ID)
	portal := u.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: u,
	}

	portal.discordMessages <- msg
}

func (u *User) messageDeleteHandler(s *discordgo.Session, m *discordgo.MessageDelete) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring message delete for guild message")

		return
	}

	key := database.NewPortalKey(m.ChannelID, u.ID)
	portal := u.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: u,
	}

	portal.discordMessages <- msg
}

func (u *User) messageUpdateHandler(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring message update for guild message")

		return
	}

	key := database.NewPortalKey(m.ChannelID, u.ID)
	portal := u.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: u,
	}

	portal.discordMessages <- msg
}

func (u *User) reactionAddHandler(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring reaction for guild message")

		return
	}

	key := database.NewPortalKey(m.ChannelID, u.User.ID)
	portal := u.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: u,
	}

	portal.discordMessages <- msg
}

func (u *User) reactionRemoveHandler(s *discordgo.Session, m *discordgo.MessageReactionRemove) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring reaction for guild message")

		return
	}

	key := database.NewPortalKey(m.ChannelID, u.User.ID)
	portal := u.bridge.GetPortalByID(key)

	msg := portalDiscordMessage{
		msg:  m,
		user: u,
	}

	portal.discordMessages <- msg
}

func (u *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) bool {
	ret := false

	inviteContent := event.Content{
		Parsed: &event.MemberEventContent{
			Membership: event.MembershipInvite,
			IsDirect:   isDirect,
		},
		Raw: map[string]interface{}{},
	}

	customPuppet := u.bridge.GetPuppetByCustomMXID(u.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		inviteContent.Raw["fi.mau.will_auto_accept"] = true
	}

	_, err := intent.SendStateEvent(roomID, event.StateMember, u.MXID.String(), &inviteContent)

	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		u.bridge.StateStore.SetMembership(roomID, u.MXID, event.MembershipJoin)
		ret = true
	} else if err != nil {
		u.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ret = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			u.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
			ret = false
		} else {
			ret = true
		}
	}

	return ret
}

func (u *User) getDirectChats() map[id.UserID][]id.RoomID {
	chats := map[id.UserID][]id.RoomID{}

	privateChats := u.bridge.db.Portal.FindPrivateChats(u.ID)
	for _, portal := range privateChats {
		if portal.MXID != "" {
			puppetMXID := u.bridge.FormatPuppetMXID(portal.Key.Receiver)

			chats[puppetMXID] = []id.RoomID{portal.MXID}
		}
	}

	return chats
}

func (u *User) updateDirectChats(chats map[id.UserID][]id.RoomID) {
	if !u.bridge.Config.Bridge.SyncDirectChatList {
		return
	}

	puppet := u.bridge.GetPuppetByMXID(u.MXID)
	if puppet == nil {
		return
	}

	intent := puppet.CustomIntent()
	if intent == nil {
		return
	}

	method := http.MethodPatch
	if chats == nil {
		chats = u.getDirectChats()
		method = http.MethodPut
	}

	u.log.Debugln("Updating m.direct list on homeserver")

	var err error
	if u.bridge.Config.Homeserver.Asmux {
		urlPath := intent.BuildBaseURL("_matrix", "client", "unstable", "com.beeper.asmux", "dms")
		_, err = intent.MakeFullRequest(mautrix.FullRequest{
			Method:      method,
			URL:         urlPath,
			Headers:     http.Header{"X-Asmux-Auth": {u.bridge.as.Registration.AppToken}},
			RequestJSON: chats,
		})
	} else {
		existingChats := map[id.UserID][]id.RoomID{}

		err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
		if err != nil {
			u.log.Warnln("Failed to get m.direct list to update it:", err)

			return
		}

		for userID, rooms := range existingChats {
			if _, ok := u.bridge.ParsePuppetMXID(userID); !ok {
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
		u.log.Warnln("Failed to update m.direct list:", err)
	}
}
