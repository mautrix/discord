package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) New() *User {
	return &User{
		db:  uq.db,
		log: uq.log,
	}
}

func (uq *UserQuery) GetByMXID(userID id.UserID) *User {
	query := `SELECT mxid, dcid, management_room, token FROM "user" WHERE mxid=$1`
	row := uq.db.QueryRow(query, userID)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetByID(id string) *User {
	query := `SELECT mxid, dcid, management_room, token FROM "user" WHERE dcid=$1`
	row := uq.db.QueryRow(query, id)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetAll() []*User {
	rows, err := uq.db.Query(`SELECT mxid, dcid, management_room, token FROM "user" WHERE token IS NOT NULL`)
	if err != nil || rows == nil {
		return nil
	}

	defer rows.Close()

	users := []*User{}
	for rows.Next() {
		users = append(users, uq.New().Scan(rows))
	}

	return users
}

type User struct {
	db  *Database
	log log.Logger

	MXID id.UserID
	ID   string

	ManagementRoom id.RoomID

	Token string
}

func (u *User) Scan(row dbutil.Scannable) *User {
	var token sql.NullString
	var discordID sql.NullString

	err := row.Scan(&u.MXID, &discordID, &u.ManagementRoom, &token)
	if err != nil {
		if err != sql.ErrNoRows {
			u.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if token.Valid {
		u.Token = token.String
	}

	if discordID.Valid {
		u.ID = discordID.String
	}

	return u
}

func (u *User) Insert() {
	query := "INSERT INTO \"user\" (mxid, dcid, management_room, token) VALUES ($1, $2, $3, $4)"

	var token sql.NullString
	var discordID sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	if u.ID != "" {
		discordID.String = u.ID
		discordID.Valid = true
	}

	_, err := u.db.Exec(query, u.MXID, discordID, u.ManagementRoom, token)

	if err != nil {
		u.log.Warnfln("Failed to insert %s: %v", u.MXID, err)
	}
}

func (u *User) Update() {
	query := "UPDATE \"user\" SET dcid=$1, management_room=$2, token=$3 WHERE mxid=$4"

	var token sql.NullString
	var discordID sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	if u.ID != "" {
		discordID.String = u.ID
		discordID.Valid = true
	}

	_, err := u.db.Exec(query, discordID, u.ManagementRoom, token, u.MXID)

	if err != nil {
		u.log.Warnfln("Failed to update %q: %v", u.MXID, err)
	}
}
