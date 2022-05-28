package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix/util/dbutil"
)

type GuildQuery struct {
	db  *Database
	log log.Logger
}

const (
	guildSelect = "SELECT dcid, mxid, name, name_set, avatar, avatar_url, avatar_set, auto_bridge_channels FROM guild"
)

func (gq *GuildQuery) New() *Guild {
	return &Guild{
		db:  gq.db,
		log: gq.log,
	}
}

func (gq *GuildQuery) GetByID(dcid string) *Guild {
	query := guildSelect + " WHERE dcid=$1"
	return gq.New().Scan(gq.db.QueryRow(query, dcid))
}

func (gq *GuildQuery) GetByMXID(mxid id.RoomID) *Guild {
	query := guildSelect + " WHERE mxid=$1"
	return gq.New().Scan(gq.db.QueryRow(query, mxid))
}

func (gq *GuildQuery) GetAll() []*Guild {
	rows, err := gq.db.Query(guildSelect)
	if err != nil {
		gq.log.Errorln("Failed to query guilds:", err)
		return nil
	}

	var guilds []*Guild
	for rows.Next() {
		guild := gq.New().Scan(rows)
		if guild != nil {
			guilds = append(guilds, guild)
		}
	}

	return guilds
}

type Guild struct {
	db  *Database
	log log.Logger

	ID        string
	MXID      id.RoomID
	Name      string
	NameSet   bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	AutoBridgeChannels bool
}

func (g *Guild) Scan(row dbutil.Scannable) *Guild {
	var mxid sql.NullString
	var avatarURL string
	err := row.Scan(&g.ID, &mxid, &g.Name, &g.NameSet, &g.Avatar, &avatarURL, &g.AvatarSet, &g.AutoBridgeChannels)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			g.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}
	g.MXID = id.RoomID(mxid.String)
	g.AvatarURL, _ = id.ParseContentURI(avatarURL)
	return g
}

func (g *Guild) mxidPtr() *id.RoomID {
	if g.MXID != "" {
		return &g.MXID
	}
	return nil
}
func (g *Guild) Insert() {
	query := `
		INSERT INTO guild (dcid, mxid, name, name_set, avatar, avatar_url, avatar_set, auto_bridge_channels)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := g.db.Exec(query, g.ID, g.mxidPtr(), g.Name, g.NameSet, g.Avatar, g.AvatarURL.String(), g.AvatarSet, g.AutoBridgeChannels)
	if err != nil {
		g.log.Warnfln("Failed to insert %s: %v", g.ID, err)
		panic(err)
	}
}

func (g *Guild) Update() {
	query := `
		UPDATE guild SET mxid=$1, name=$2, name_set=$3, avatar=$4, avatar_url=$5, avatar_set=$6, auto_bridge_channels=$7
		WHERE dcid=$8
	`
	_, err := g.db.Exec(query, g.mxidPtr(), g.Name, g.NameSet, g.Avatar, g.AvatarURL.String(), g.AvatarSet, g.AutoBridgeChannels, g.ID)
	if err != nil {
		g.log.Warnfln("Failed to update %s: %v", g.ID, err)
		panic(err)
	}
}

func (g *Guild) Delete() {
	_, err := g.db.Exec("DELETE FROM guild WHERE dcid=$1", g.ID)
	if err != nil {
		g.log.Warnfln("Failed to delete %s: %v", g.ID, err)
		panic(err)
	}
}
