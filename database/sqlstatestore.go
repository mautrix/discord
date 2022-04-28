package database

import (
	"database/sql"
	"encoding/json"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type SQLStateStore struct {
	*appservice.TypingStateStore

	db  *Database
	log log.Logger

	Typing     map[id.RoomID]map[id.UserID]int64
	typingLock sync.RWMutex
}

// make sure that SQLStateStore implements the appservice.StateStore interface
var _ appservice.StateStore = (*SQLStateStore)(nil)

func NewSQLStateStore(db *Database) *SQLStateStore {
	return &SQLStateStore{
		TypingStateStore: appservice.NewTypingStateStore(),
		db:               db,
		log:              db.log.Sub("StateStore"),
	}
}

func (s *SQLStateStore) IsRegistered(userID id.UserID) bool {
	var isRegistered bool

	query := "SELECT EXISTS(SELECT 1 FROM mx_registrations WHERE user_id=$1)"
	row := s.db.QueryRow(query, userID)

	err := row.Scan(&isRegistered)
	if err != nil {
		s.log.Warnfln("Failed to scan registration existence for %s: %v", userID, err)
	}

	return isRegistered
}

func (s *SQLStateStore) MarkRegistered(userID id.UserID) {
	query := "INSERT INTO mx_registrations (user_id) VALUES ($1)" +
		" ON CONFLICT (user_id) DO NOTHING"

	_, err := s.db.Exec(query, userID)
	if err != nil {
		s.log.Warnfln("Failed to mark %s as registered: %v", userID, err)
	}
}

func (s *SQLStateStore) IsTyping(roomID id.RoomID, userID id.UserID) bool {
	s.log.Debugln("IsTyping")

	return false
}

func (s *SQLStateStore) SetTyping(roomID id.RoomID, userID id.UserID, timeout int64) {
	s.log.Debugln("SetTyping")
}

func (s *SQLStateStore) IsInRoom(roomID id.RoomID, userID id.UserID) bool {
	return s.IsMembership(roomID, userID, "join")
}

func (s *SQLStateStore) IsInvited(roomID id.RoomID, userID id.UserID) bool {
	return s.IsMembership(roomID, userID, "join", "invite")
}

func (s *SQLStateStore) IsMembership(roomID id.RoomID, userID id.UserID, allowedMemberships ...event.Membership) bool {
	membership := s.GetMembership(roomID, userID)
	for _, allowedMembership := range allowedMemberships {
		if allowedMembership == membership {
			return true
		}
	}

	return false
}

func (s *SQLStateStore) GetMembership(roomID id.RoomID, userID id.UserID) event.Membership {
	query := "SELECT membership FROM mx_user_profile WHERE " +
		"room_id=$1 AND user_id=$2"
	row := s.db.QueryRow(query, roomID, userID)

	membership := event.MembershipLeave
	err := row.Scan(&membership)
	if err != nil && err != sql.ErrNoRows {
		s.log.Warnfln("Failed to scan membership of %s in %s: %v", userID, roomID, err)
	}

	return membership
}

func (s *SQLStateStore) GetMember(roomID id.RoomID, userID id.UserID) *event.MemberEventContent {
	member, ok := s.TryGetMember(roomID, userID)
	if !ok {
		member.Membership = event.MembershipLeave
	}

	return member
}

func (s *SQLStateStore) TryGetMember(roomID id.RoomID, userID id.UserID) (*event.MemberEventContent, bool) {
	query := "SELECT membership, displayname, avatar_url FROM mx_user_profile " +
		"WHERE room_id=$1 AND user_id=$2"
	row := s.db.QueryRow(query, roomID, userID)

	var member event.MemberEventContent
	err := row.Scan(&member.Membership, &member.Displayname, &member.AvatarURL)
	if err != nil && err != sql.ErrNoRows {
		s.log.Warnfln("Failed to scan member info of %s in %s: %v", userID, roomID, err)
	}

	return &member, err == nil
}

func (s *SQLStateStore) SetMembership(roomID id.RoomID, userID id.UserID, membership event.Membership) {
	query := "INSERT INTO mx_user_profile (room_id, user_id, membership)" +
		" VALUES ($1, $2, $3) ON CONFLICT (room_id, user_id) DO UPDATE SET" +
		" membership=excluded.membership"

	_, err := s.db.Exec(query, roomID, userID, membership)
	if err != nil {
		s.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, membership, err)
	}
}

func (s *SQLStateStore) SetMember(roomID id.RoomID, userID id.UserID, member *event.MemberEventContent) {
	query := "INSERT INTO mx_user_profile" +
		" (room_id, user_id, membership, displayname, avatar_url)" +
		" VALUES ($1, $2, $3, $4, $5) ON CONFLICT (room_id, user_id)" +
		" DO UPDATE SET membership=excluded.membership," +
		" displayname=excluded.displayname, avatar_url=excluded.avatar_url"
	_, err := s.db.Exec(query, roomID, userID, member.Membership, member.Displayname, member.AvatarURL)
	if err != nil {
		s.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, member, err)
	}
}

func (s *SQLStateStore) SetPowerLevels(roomID id.RoomID, levels *event.PowerLevelsEventContent) {
	levelsBytes, err := json.Marshal(levels)
	if err != nil {
		s.log.Errorfln("Failed to marshal power levels of %s: %v", roomID, err)
		return
	}

	query := "INSERT INTO mx_room_state (room_id, power_levels)" +
		" VALUES ($1, $2) ON CONFLICT (room_id) DO UPDATE SET" +
		" power_levels=excluded.power_levels"
	_, err = s.db.Exec(query, roomID, levelsBytes)
	if err != nil {
		s.log.Warnfln("Failed to store power levels of %s: %v", roomID, err)
	}
}

func (s *SQLStateStore) GetPowerLevels(roomID id.RoomID) *event.PowerLevelsEventContent {
	query := "SELECT power_levels FROM mx_room_state WHERE room_id=$1"
	row := s.db.QueryRow(query, roomID)
	if row == nil {
		return nil
	}

	var data []byte
	err := row.Scan(&data)
	if err != nil {
		s.log.Errorfln("Failed to scan power levels of %s: %v", roomID, err)

		return nil
	}

	levels := &event.PowerLevelsEventContent{}
	err = json.Unmarshal(data, levels)
	if err != nil {
		s.log.Errorfln("Failed to parse power levels of %s: %v", roomID, err)

		return nil
	}

	return levels
}

func (s *SQLStateStore) GetPowerLevel(roomID id.RoomID, userID id.UserID) int {
	if s.db.dialect == "postgres" {
		query := "SELECT COALESCE((power_levels->'users'->$2)::int," +
			" (power_levels->'users_default')::int, 0)" +
			" FROM mx_room_state WHERE room_id=$1"
		row := s.db.QueryRow(query, roomID, userID)
		if row == nil {
			// Power levels not in db
			return 0
		}

		var powerLevel int
		err := row.Scan(&powerLevel)
		if err != nil {
			s.log.Errorfln("Failed to scan power level of %s in %s: %v", userID, roomID, err)
		}

		return powerLevel
	}

	return s.GetPowerLevels(roomID).GetUserLevel(userID)
}

func (s *SQLStateStore) GetPowerLevelRequirement(roomID id.RoomID, eventType event.Type) int {
	if s.db.dialect == "postgres" {
		defaultType := "events_default"
		defaultValue := 0
		if eventType.IsState() {
			defaultType = "state_default"
			defaultValue = 50
		}

		query := "SELECT COALESCE((power_levels->'events'->$2)::int," +
			" (power_levels->'$3')::int, $4)" +
			" FROM mx_room_state WHERE room_id=$1"
		row := s.db.QueryRow(query, roomID, eventType.Type, defaultType, defaultValue)
		if row == nil {
			// Power levels not in db
			return defaultValue
		}

		var powerLevel int
		err := row.Scan(&powerLevel)
		if err != nil {
			s.log.Errorfln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}

		return powerLevel
	}

	return s.GetPowerLevels(roomID).GetEventLevel(eventType)
}

func (s *SQLStateStore) HasPowerLevel(roomID id.RoomID, userID id.UserID, eventType event.Type) bool {
	if s.db.dialect == "postgres" {
		defaultType := "events_default"
		defaultValue := 0
		if eventType.IsState() {
			defaultType = "state_default"
			defaultValue = 50
		}

		query := "SELECT COALESCE((power_levels->'users'->$2)::int," +
			" (power_levels->'users_default')::int, 0) >=" +
			" COALESCE((power_levels->'events'->$3)::int," +
			" (power_levels->'$4')::int, $5)" +
			" FROM mx_room_state WHERE room_id=$1"
		row := s.db.QueryRow(query, roomID, userID, eventType.Type, defaultType, defaultValue)
		if row == nil {
			// Power levels not in db
			return defaultValue == 0
		}

		var hasPower bool
		err := row.Scan(&hasPower)
		if err != nil {
			s.log.Errorfln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}

		return hasPower
	}

	return s.GetPowerLevel(roomID, userID) >= s.GetPowerLevelRequirement(roomID, eventType)
}

func (store *SQLStateStore) FindSharedRooms(userID id.UserID) []id.RoomID {
	query := `
		SELECT room_id FROM mx_user_profile
		LEFT JOIN portal ON portal.mxid=mx_user_profile.room_id
		WHERE user_id=$1 AND portal.encrypted=true
	`

	rooms := []id.RoomID{}

	rows, err := store.db.Query(query, userID)
	if err != nil {
		store.log.Warnfln("Failed to query shared rooms with %s: %v", userID, err)

		return rooms
	}

	for rows.Next() {
		var roomID id.RoomID

		err = rows.Scan(&roomID)
		if err != nil {
			store.log.Warnfln("Failed to scan room ID: %v", err)
		} else {
			rooms = append(rooms, roomID)
		}
	}

	return rooms
}
