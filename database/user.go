package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type User struct {
	db  *Database
	log log.Logger

	MXID id.UserID
	ID   string

	ManagementRoom id.RoomID

	Token string
}

func (u *User) Scan(row Scannable) *User {
	var token sql.NullString

	err := row.Scan(&u.MXID, &u.ID, &u.ManagementRoom, &token)
	if err != nil {
		if err != sql.ErrNoRows {
			u.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if token.Valid {
		u.Token = token.String
	}

	return u
}

func (u *User) Insert() {
	query := "INSERT INTO \"user\"" +
		" (mxid, id, management_room, token)" +
		" VALUES ($1, $2, $3, $4);"

	var token sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	_, err := u.db.Exec(query, u.MXID, u.ID, u.ManagementRoom, token)

	if err != nil {
		u.log.Warnfln("Failed to insert %s: %v", u.MXID, err)
	}
}

func (u *User) Update() {
	query := "UPDATE \"user\" SET" +
		" id=$1, management_room=$2, token=$3" +
		" WHERE mxid=$4;"

	var token sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	_, err := u.db.Exec(query, u.ID, u.ManagementRoom, token, u.MXID)

	if err != nil {
		u.log.Warnfln("Failed to update %q: %v", u.MXID, err)
	}
}
