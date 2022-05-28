package database

import (
	"database/sql"
	"errors"
	"time"

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
	query := `SELECT mxid, dcid, discord_token, management_room, space_room FROM "user" WHERE mxid=$1`
	return uq.New().Scan(uq.db.QueryRow(query, userID))
}

func (uq *UserQuery) GetByID(id string) *User {
	query := `SELECT mxid, dcid, discord_token, management_room, space_room FROM "user" WHERE dcid=$1`
	return uq.New().Scan(uq.db.QueryRow(query, id))
}

func (uq *UserQuery) GetAllWithToken() []*User {
	query := `
		SELECT mxid, dcid, discord_token, management_room, space_room
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
	db  *Database
	log log.Logger

	MXID           id.UserID
	DiscordID      string
	DiscordToken   string
	ManagementRoom id.RoomID
	SpaceRoom      id.RoomID
}

type UserGuild struct {
	GuildID   string
	Timestamp time.Time
	InSpace   bool
}

func (u *User) GetGuilds() []UserGuild {
	res, err := u.db.Query("SELECT guild_id, timestamp, in_space FROM user_guild WHERE user_mxid=$1", u.MXID)
	if err != nil {
		u.log.Errorln("Failed to get guilds:", err)
		panic(err)
		return nil
	}
	var guilds []UserGuild
	for res.Next() {
		var guild UserGuild
		var ts int64
		err = res.Scan(&guild.GuildID, &ts, &guild.InSpace)
		if err != nil {
			u.log.Errorln("Error scanning user guild:", err)
			panic(err)
		} else {
			guild.Timestamp = time.UnixMilli(ts)
			guilds = append(guilds, guild)
		}
	}
	return guilds
}

func (u *User) IsInSpace(guildID string) (isIn bool) {
	query := `SELECT in_space FROM user_guild WHERE user_mxid=$1 AND guild_id=$2`
	err := u.db.QueryRow(query, u.MXID, guildID).Scan(&isIn)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		u.log.Warnfln("Failed to scan in_space for %s/%s: %v", u.MXID, guildID, err)
		panic(err)
	}
	return
}

func (u *User) MarkInGuild(guild UserGuild) {
	query := `
		INSERT INTO user_guild (guild_id, user_mxid, timestamp, in_space)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (guild_id, user_mxid) DO UPDATE
		    SET timestamp=excluded.timestamp, in_space=excluded.in_space
	`
	_, err := u.db.Exec(query, guild.GuildID, u.MXID, guild.Timestamp.UnixMilli(), guild.InSpace)
	if err != nil {
		u.log.Errorfln("Failed to insert user guild %s/%s: %v", u.MXID, guild.GuildID, err)
		panic(err)
	}
}

func (u *User) MarkNotInGuild(guildID string) {
	query := `DELETE FROM user_guild WHERE user_mxid=$1 AND guild_id=$2`
	_, err := u.db.Exec(query, u.MXID, guildID)
	if err != nil {
		u.log.Errorfln("Failed to remove user guild %s/%s: %v", u.MXID, guildID, err)
		panic(err)
	}
}

func (u *User) PruneGuildList(beforeTS time.Time) {
	_, err := u.db.Exec("DELETE FROM user_guild WHERE user_mxid=$1 AND timestamp<$2", u.MXID, beforeTS.UnixMilli())
	if err != nil {
		u.log.Errorln("Failed to prune user guild list:", err)
		panic(err)
	}
}

func (u *User) Scan(row dbutil.Scannable) *User {
	var discordID, managementRoom, spaceRoom, discordToken sql.NullString
	err := row.Scan(&u.MXID, &discordID, &discordToken, &managementRoom, &spaceRoom)
	if err != nil {
		if err != sql.ErrNoRows {
			u.log.Errorln("Database scan failed:", err)
			panic(err)
		}
		return nil
	}
	u.DiscordID = discordID.String
	u.DiscordToken = discordToken.String
	u.ManagementRoom = id.RoomID(managementRoom.String)
	u.SpaceRoom = id.RoomID(spaceRoom.String)
	return u
}

func (u *User) Insert() {
	query := `INSERT INTO "user" (mxid, dcid, discord_token, management_room, space_room) VALUES ($1, $2, $3, $4, $5)`
	_, err := u.db.Exec(query, u.MXID, strPtr(u.DiscordID), strPtr(u.DiscordToken), strPtr(string(u.ManagementRoom)), strPtr(string(u.SpaceRoom)))
	if err != nil {
		u.log.Warnfln("Failed to insert %s: %v", u.MXID, err)
		panic(err)
	}
}

func (u *User) Update() {
	query := `UPDATE "user" SET dcid=$1, discord_token=$2, management_room=$3, space_room=$4 WHERE mxid=$5`
	_, err := u.db.Exec(query, strPtr(u.DiscordID), strPtr(u.DiscordToken), strPtr(string(u.ManagementRoom)), strPtr(string(u.SpaceRoom)), u.MXID)
	if err != nil {
		u.log.Warnfln("Failed to update %q: %v", u.MXID, err)
		panic(err)
	}
}
