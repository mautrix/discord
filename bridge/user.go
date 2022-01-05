package bridge

import (
	"github.com/bwmarrin/discordgo"
	"github.com/skip2/go-qrcode"

	log "maunium.net/go/maulogger/v2"
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

func (b *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: b,
		log:    b.log.Sub("User").Sub(string(dbUser.MXID)),
	}

	return user
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

func (u *User) login(token string) error {
	err := u.User.Login(token)
	if err != nil {
		return err
	}

	u.User.Session.AddHandler(u.messageHandler)

	u.log.Warnln("logged in, opening websocket")

	return u.User.Session.Open()
}

func (u *User) messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	u.log.Warnln("received message", m)
}
