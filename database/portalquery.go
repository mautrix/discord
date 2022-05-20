package database

import (
	"github.com/bwmarrin/discordgo"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const (
	portalSelect = "SELECT channel_id, receiver, mxid, name, topic, avatar," +
		" avatar_url, type, dmuser, first_event_id, encrypted" +
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
	return pq.get(portalSelect+" WHERE channel_id=$1 AND receiver=$2", key.ChannelID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(portalSelect+" WHERE mxid=$1", mxid)
}

func (pq *PortalQuery) GetAllByID(id string) []*Portal {
	return pq.getAll(portalSelect+" WHERE receiver=$1", id)
}

func (pq *PortalQuery) FindPrivateChats(receiver string) []*Portal {
	query := portalSelect + " portal WHERE receiver=$1 AND type=$2;"

	return pq.getAll(query, receiver, discordgo.ChannelTypeDM)
}

func (pq *PortalQuery) getAll(query string, args ...interface{}) []*Portal {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	portals := []*Portal{}
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
