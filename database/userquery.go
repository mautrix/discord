package database

import (
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
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
	query := `SELECT mxid, id, management_room, token FROM "user" WHERE mxid=$1`
	row := uq.db.QueryRow(query, userID)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetByID(id string) *User {
	query := `SELECT mxid, id, management_room, token FROM "user" WHERE id=$1`
	row := uq.db.QueryRow(query, id)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetAll() []*User {
	rows, err := uq.db.Query(`SELECT mxid, id, management_room, token FROM "user" WHERE token IS NOT NULL`)
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
