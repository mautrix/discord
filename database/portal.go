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

	FirstEventID id.EventID
}

func (p *Portal) Scan(row Scannable) *Portal {
	var mxid, avatarURL, firstEventID sql.NullString

	err := row.Scan(&p.Key.ID, &p.Key.ChannelID, &mxid, &p.Name, &p.Topic, &p.Avatar, &avatarURL, &firstEventID)
	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.FirstEventID = id.EventID(firstEventID.String)

	return p
}

func (p *Portal) Insert() {
	query := "INSERT INTO portal" +
		" (id, mxid, channel_id, name, topic, avatar, avatar_url, first_event_id)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8)"

	_, err := p.db.Exec(query, p.Key.ID, p.MXID, p.Key.ChannelID,
		p.Name, p.Topic, p.Avatar, p.AvatarURL.String(), p.FirstEventID.String())

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}

func (p *Portal) Update() {
	query := "UPDATE portal SET" +
		" mxid=$1, name=$2, topic=$3, avatar=$4, avatar_url=$5, first_event_id=$6" +
		" WHERE id=$7 AND channel_id=$8"

	_, err := p.db.Exec(query, p.MXID, p.Name, p.Topic, p.Avatar,
		p.AvatarURL.String(), p.FirstEventID.String(), p.Key.ID,
		p.Key.ChannelID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
	}
}
