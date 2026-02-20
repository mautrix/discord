package database

import (
	"database/sql"
	"errors"
	"time"

	"go.mau.fi/util/dbutil"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const (
	UserPortalTypeDM     = "dm"
	UserPortalTypeGuild  = "guild"
	UserPortalTypeThread = "thread"
)

type UserPortal struct {
	DiscordID string
	Type      string
	Timestamp time.Time
	InSpace   bool
}

func (up UserPortal) Scan(l log.Logger, row dbutil.Scannable) *UserPortal {
	var ts int64
	err := row.Scan(&up.DiscordID, &up.Type, &ts, &up.InSpace)
	if err != nil {
		l.Errorln("Error scanning user portal:", err)
		panic(err)
	}
	up.Timestamp = time.UnixMilli(ts).UTC()
	return &up
}

func (u *User) scanUserPortals(rows dbutil.Rows) []UserPortal {
	var ups []UserPortal
	for rows.Next() {
		up := UserPortal{}.Scan(u.log, rows)
		if up != nil {
			ups = append(ups, *up)
		}
	}
	return ups
}

func (db *Database) GetUsersInPortal(channelID string) []id.UserID {
	rows, err := db.Query("SELECT user_mxid FROM user_portal WHERE discord_id=$1", channelID)
	if err != nil {
		db.Portal.log.Errorln("Failed to get users in portal:", err)
	}
	var users []id.UserID
	for rows.Next() {
		var mxid id.UserID
		err = rows.Scan(&mxid)
		if err != nil {
			db.Portal.log.Errorln("Failed to scan user in portal:", err)
		} else {
			users = append(users, mxid)
		}
	}
	return users
}

func (u *User) GetPortals() []UserPortal {
	rows, err := u.db.Query("SELECT discord_id, type, timestamp, in_space FROM user_portal WHERE user_mxid=$1", u.MXID)
	if err != nil {
		u.log.Errorln("Failed to get portals:", err)
		panic(err)
	}
	return u.scanUserPortals(rows)
}

func (u *User) IsInSpace(discordID string) (isIn bool) {
	query := `SELECT in_space FROM user_portal WHERE user_mxid=$1 AND discord_id=$2`
	err := u.db.QueryRow(query, u.MXID, discordID).Scan(&isIn)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		u.log.Warnfln("Failed to scan in_space for %s/%s: %v", u.MXID, discordID, err)
		panic(err)
	}
	return
}

func (u *User) IsInPortal(discordID string) (isIn bool) {
	query := `SELECT EXISTS(SELECT 1 FROM user_portal WHERE user_mxid=$1 AND discord_id=$2)`
	err := u.db.QueryRow(query, u.MXID, discordID).Scan(&isIn)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		u.log.Warnfln("Failed to scan in_space for %s/%s: %v", u.MXID, discordID, err)
		panic(err)
	}
	return
}

func (u *User) MarkInPortal(portal UserPortal) {
	query := `
		INSERT INTO user_portal (discord_id, type, user_mxid, timestamp, in_space)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (discord_id, user_mxid) DO UPDATE
		    SET timestamp=excluded.timestamp, in_space=excluded.in_space
	`
	_, err := u.db.Exec(query, portal.DiscordID, portal.Type, u.MXID, portal.Timestamp.UnixMilli(), portal.InSpace)
	if err != nil {
		u.log.Errorfln("Failed to insert user portal %s/%s: %v", u.MXID, portal.DiscordID, err)
		panic(err)
	}
}

func (u *User) MarkNotInPortal(discordID string) {
	query := `DELETE FROM user_portal WHERE user_mxid=$1 AND discord_id=$2`
	_, err := u.db.Exec(query, u.MXID, discordID)
	if err != nil {
		u.log.Errorfln("Failed to remove user portal %s/%s: %v", u.MXID, discordID, err)
		panic(err)
	}
}

func (u *User) DeleteAllPortalAssociations() {
	query := `DELETE FROM user_portal WHERE user_mxid=$1`
	_, err := u.db.Exec(query, u.MXID)
	if err != nil {
		u.log.Errorfln("Failed to delete all portal associations for %s: %v", u.MXID, err)
		panic(err)
	}
}

func (u *User) PortalHasOtherUsers(discordID string) (hasOtherUsers bool) {
	query := `SELECT COUNT(*) > 0 FROM user_portal WHERE user_mxid<>$1 AND discord_id=$2`
	err := u.db.QueryRow(query, u.MXID, discordID).Scan(&hasOtherUsers)
	if err != nil {
		u.log.Errorfln("Failed to check if %s has users other than %s: %v", discordID, u.MXID, err)
		panic(err)
	}
	return
}

func (u *User) PrunePortalList(beforeTS time.Time) []UserPortal {
	query := `
		DELETE FROM user_portal
		WHERE user_mxid=$1 AND timestamp<$2 AND type IN ('dm', 'guild')
		RETURNING discord_id, type, timestamp, in_space
	`
	rows, err := u.db.Query(query, u.MXID, beforeTS.UnixMilli())
	if err != nil {
		u.log.Errorln("Failed to prune user guild list:", err)
		panic(err)
	}
	return u.scanUserPortals(rows)
}
