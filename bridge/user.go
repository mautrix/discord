package bridge

import (
	log "maunium.net/go/maulogger/v2"
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
