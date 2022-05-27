package database

import (
	"database/sql"

	"github.com/bwmarrin/discordgo"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

const (
	portalSelect = "SELECT dcid, receiver, mxid, name, topic, avatar," +
		" avatar_url, type, other_user_id, first_event_id, encrypted" +
		" FROM portal"
)

type PortalQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PortalQuery) New() *Portal {
	return &Portal{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll(portalSelect)
}

func (pq *PortalQuery) GetByID(key PortalKey) *Portal {
	return pq.get(portalSelect+" WHERE dcid=$1 AND receiver=$2", key.ChannelID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(portalSelect+" WHERE mxid=$1", mxid)
}

func (pq *PortalQuery) FindPrivateChatsWith(id string) []*Portal {
	return pq.getAll(portalSelect+" WHERE other_user_id=$1 AND type=$2", id, discordgo.ChannelTypeDM)
}

func (pq *PortalQuery) FindPrivateChatsOf(receiver string) []*Portal {
	query := portalSelect + " portal WHERE receiver=$1 AND type=$2;"

	return pq.getAll(query, receiver, discordgo.ChannelTypeDM)
}

func (pq *PortalQuery) getAll(query string, args ...interface{}) []*Portal {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	var portals []*Portal
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}

	return portals
}

func (pq *PortalQuery) get(query string, args ...interface{}) *Portal {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return pq.New().Scan(row)
}

type Portal struct {
	db  *Database
	log log.Logger

	Key         PortalKey
	Type        discordgo.ChannelType
	OtherUserID string

	MXID id.RoomID

	Name      string
	Topic     string
	Avatar    string
	AvatarURL id.ContentURI
	Encrypted bool

	FirstEventID id.EventID
}

func (p *Portal) Scan(row dbutil.Scannable) *Portal {
	var mxid, avatarURL, firstEventID sql.NullString
	var typ sql.NullInt32

	err := row.Scan(&p.Key.ChannelID, &p.Key.Receiver, &mxid, &p.Name,
		&p.Topic, &p.Avatar, &avatarURL, &typ, &p.OtherUserID, &firstEventID,
		&p.Encrypted)

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

func (p *Portal) mxidPtr() *id.RoomID {
	if p.MXID != "" {
		return &p.MXID
	}

	return nil
}

func (p *Portal) Insert() {
	query := "INSERT INTO portal" +
		" (dcid, receiver, mxid, name, topic, avatar, avatar_url," +
		" type, other_user_id, first_event_id, encrypted)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)"

	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver, p.mxidPtr(),
		p.Name, p.Topic, p.Avatar, p.AvatarURL.String(), p.Type, p.OtherUserID,
		p.FirstEventID.String(), p.Encrypted)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}

func (p *Portal) Update() {
	query := "UPDATE portal SET" +
		" mxid=$1, name=$2, topic=$3, avatar=$4, avatar_url=$5, type=$6," +
		" other_user_id=$7, first_event_id=$8, encrypted=$9" +
		" WHERE dcid=$10 AND receiver=$11"

	_, err := p.db.Exec(query, p.mxidPtr(), p.Name, p.Topic, p.Avatar,
		p.AvatarURL.String(), p.Type, p.OtherUserID, p.FirstEventID.String(),
		p.Encrypted,
		p.Key.ChannelID, p.Key.Receiver)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
	}
}

func (p *Portal) Delete() {
	query := "DELETE FROM portal WHERE dcid=$1 AND receiver=$2"
	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver)
	if err != nil {
		p.log.Warnfln("Failed to delete %s: %v", p.Key, err)
	}
}
