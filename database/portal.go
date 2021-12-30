package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type Portal struct {
	db  *Database
	log log.Logger

	Key  PortalKey
	MXID id.RoomID

	Name  string
	Topic string

	Avatar    string
	AvatarURL id.ContentURI
}

func (p *Portal) Scan(row Scannable) *Portal {
	var mxid, avatarURL sql.NullString

	err := row.Scan(&p.Key.ID, &p.Key.Receiver, &mxid, &p.Name, &p.Topic, &p.Avatar, &avatarURL)
	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)

	return p
}

func (p *Portal) Insert() {
	query := "INSERT INTO portal" +
		" (id, receiver, mxid, name, topic, avatar, avatar_url)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7)"

	_, err := p.db.Exec(query, p.Key.ID, p.Key.Receiver, p.MXID,
		p.Name, p.Topic, p.Avatar, p.AvatarURL.String())

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}
