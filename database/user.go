// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package database

import (
	"database/sql"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type UserQuery struct {
	*dbutil.QueryHelper[*User]
}

func newUser(qh *dbutil.QueryHelper[*User]) *User {
	return &User{qh: qh}
}

func (uq *UserQuery) GetByMXID(userID id.UserID) *User {
	query := `SELECT mxid, dcid, discord_token, management_room, space_room, dm_space_room, read_state_version FROM "user" WHERE mxid=$1`
	return uq.New().Scan(uq.db.QueryRow(query, userID))
}

func (uq *UserQuery) GetByID(id string) *User {
	query := `SELECT mxid, dcid, discord_token, management_room, space_room, dm_space_room, read_state_version FROM "user" WHERE dcid=$1`
	return uq.New().Scan(uq.db.QueryRow(query, id))
}

func (uq *UserQuery) GetAllWithToken() []*User {
	query := `
		SELECT mxid, dcid, discord_token, management_room, space_room, dm_space_room, read_state_version
		FROM "user" WHERE discord_token IS NOT NULL
	`
	rows, err := uq.db.Query(query)
	if err != nil || rows == nil {
		return nil
	}

	var users []*User
	for rows.Next() {
		user := uq.New().Scan(rows)
		if user != nil {
			users = append(users, user)
		}
	}
	return users
}

type User struct {
	qh *dbutil.QueryHelper[*User]

	MXID           id.UserID
	DiscordID      string
	DiscordToken   string
	ManagementRoom id.RoomID
	SpaceRoom      id.RoomID
	DMSpaceRoom    id.RoomID

	ReadStateVersion int
}

func (u *User) Scan(row dbutil.Scannable) (*User, error) {
	var discordID, managementRoom, spaceRoom, dmSpaceRoom, discordToken sql.NullString
	err := row.Scan(&u.MXID, &discordID, &discordToken, &managementRoom, &spaceRoom, &dmSpaceRoom, &u.ReadStateVersion)
	if err != nil {
		return nil, err
	}
	u.DiscordID = discordID.String
	u.DiscordToken = discordToken.String
	u.ManagementRoom = id.RoomID(managementRoom.String)
	u.SpaceRoom = id.RoomID(spaceRoom.String)
	u.DMSpaceRoom = id.RoomID(dmSpaceRoom.String)
	return u, nil
}

func (u *User) Insert() {
	query := `INSERT INTO "user" (mxid, dcid, discord_token, management_room, space_room, dm_space_room, read_state_version) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := u.db.Exec(query, u.MXID, strPtr(u.DiscordID), strPtr(u.DiscordToken), strPtr(string(u.ManagementRoom)), strPtr(string(u.SpaceRoom)), strPtr(string(u.DMSpaceRoom)), u.ReadStateVersion)
	if err != nil {
		u.log.Warnfln("Failed to insert %s: %v", u.MXID, err)
		panic(err)
	}
}

func (u *User) Update() {
	query := `UPDATE "user" SET dcid=$1, discord_token=$2, management_room=$3, space_room=$4, dm_space_room=$5, read_state_version=$6 WHERE mxid=$7`
	_, err := u.db.Exec(query, strPtr(u.DiscordID), strPtr(u.DiscordToken), strPtr(string(u.ManagementRoom)), strPtr(string(u.SpaceRoom)), strPtr(string(u.DMSpaceRoom)), u.ReadStateVersion, u.MXID)
	if err != nil {
		u.log.Warnfln("Failed to update %q: %v", u.MXID, err)
		panic(err)
	}
}
