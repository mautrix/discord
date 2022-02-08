package database

import (
	"database/sql"

	"github.com/bwmarrin/discordgo"

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

	Type   discordgo.ChannelType
	DMUser string

	FirstEventID id.EventID
}

func (p *Portal) Scan(row Scannable) *Portal {
	var mxid, avatarURL, firstEventID sql.NullString
	var typ sql.NullInt32

	err := row.Scan(&p.Key.ChannelID, &p.Key.Receiver, &mxid, &p.Name,
		&p.Topic, &p.Avatar, &avatarURL, &typ, &p.DMUser, &firstEventID)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.Type = discordgo.ChannelType(typ.Int32)
	p.FirstEventID = id.EventID(firstEventID.String)

	return p
}

func (p *Portal) Insert() {
	query := "INSERT INTO portal" +
		" (channel_id, receiver, mxid, name, topic, avatar, avatar_url," +
		" type, dmuser, first_event_id)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)"

	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver, p.MXID,
		p.Name, p.Topic, p.Avatar, p.AvatarURL.String(), p.Type, p.DMUser,
		p.FirstEventID.String())

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}

func (p *Portal) Update() {
	query := "UPDATE portal SET" +
		" mxid=$1, name=$2, topic=$3, avatar=$4, avatar_url=$5, type=$6," +
		" dmuser=$7, first_event_id=$8" +
		" WHERE channel_id=$9 AND receiver=$10"

	_, err := p.db.Exec(query, p.MXID, p.Name, p.Topic, p.Avatar,
		p.AvatarURL.String(), p.Type, p.DMUser, p.FirstEventID.String(),
		p.Key.ChannelID, p.Key.Receiver)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
	}
}

func (p *Portal) Delete() {
	query := "DELETE FROM portal WHERE channel_id=$1 AND receiver=$2"
	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver)
	if err != nil {
		p.log.Warnfln("Failed to delete %s: %v", p.Key, err)
	}
}
