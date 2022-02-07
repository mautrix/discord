package bridge

import (
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/skip2/go-qrcode"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/database"
)

type User struct {
	*database.User

	bridge *Bridge
	log    log.Logger
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
		// if user.ID != "" {
		// 	haveSessions = true
		// }

		go user.Connect()
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

func (u *User) HasSession() bool {
	return u.User.Session != nil
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

func (u *User) Login(token string) error {
	err := u.User.NewSession(token)
	if err != nil {
		return err
	}

	return u.Connect()
}

func (u *User) Connect() error {
	u.log.Debugln("connecting to discord")

	// get our user info
	user, err := u.User.Session.User("@me")
	if err != nil {
		return err
	}

	u.User.ID = user.ID

	// Add our event handlers
	u.User.Session.AddHandler(u.connectedHandler)
	u.User.Session.AddHandler(u.disconnectedHandler)

	u.User.Session.AddHandler(u.channelCreateHandler)
	u.User.Session.AddHandler(u.channelDeleteHandler)
	u.User.Session.AddHandler(u.channelPinsUpdateHandler)
	u.User.Session.AddHandler(u.channelUpdateHandler)

	u.User.Session.AddHandler(u.messageHandler)
	u.User.Session.AddHandler(u.reactionHandler)

	// u.User.Session.Identify.Capabilities = 125
	// // Setup our properties
	// u.User.Session.Identify.Properties = discordgo.IdentifyProperties{
	// 	OS:                "Windows",
	// 	OSVersion:         "10",
	// 	Browser:           "Chrome",
	// 	BrowserUserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.159 Safari/537.36",
	// 	BrowserVersion:    "92.0.4515.159",
	// 	Referrer:          "https://discord.com/channels/@me",
	// 	ReferringDomain:   "discord.com",
	// 	ClientBuildNumber: "83364",
	// 	ReleaseChannel:    "stable",
	// }

	u.User.Session.Identify.Presence.Status = "online"

	return u.User.Session.Open()
}

func (u *User) connectedHandler(s *discordgo.Session, c *discordgo.Connect) {
	u.log.Debugln("connected to discord")
}

func (u *User) disconnectedHandler(s *discordgo.Session, d *discordgo.Disconnect) {
	u.log.Debugln("disconnected from discord")
}

func (u *User) channelCreateHandler(s *discordgo.Session, c *discordgo.ChannelCreate) {
	key := database.NewPortalKey(c.ID, u.User.ID)
	portal := u.bridge.GetPortalByID(key)

	portal.Name = c.Name
	portal.Topic = c.Topic

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

func (u *User) messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID != "" {
		u.log.Debugln("ignoring message for guild")

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

func (u *User) reactionHandler(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
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

	return ret
}
