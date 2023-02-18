package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix/util/dbutil"
)

type GuildBridgingMode int

const (
	// GuildBridgeNothing tells the bridge to never bridge messages, not even checking if a portal exists.
	GuildBridgeNothing GuildBridgingMode = iota
	// GuildBridgeIfPortalExists tells the bridge to bridge messages in channels that already have portals.
	GuildBridgeIfPortalExists
	// GuildBridgeCreateOnMessage tells the bridge to create portals as soon as a message is received.
	GuildBridgeCreateOnMessage
	// GuildBridgeEverything tells the bridge to proactively create portals on startup and when receiving channel create notifications.
	GuildBridgeEverything

	GuildBridgeInvalid GuildBridgingMode = -1
)

func ParseGuildBridgingMode(str string) GuildBridgingMode {
	str = strings.ToLower(str)
	str = strings.ReplaceAll(str, "-", "")
	str = strings.ReplaceAll(str, "_", "")
	switch str {
	case "nothing", "0":
		return GuildBridgeNothing
	case "ifportalexists", "1":
		return GuildBridgeIfPortalExists
	case "createonmessage", "2":
		return GuildBridgeCreateOnMessage
	case "everything", "3":
		return GuildBridgeEverything
	default:
		return GuildBridgeInvalid
	}
}

func (gbm GuildBridgingMode) String() string {
	switch gbm {
	case GuildBridgeNothing:
		return "nothing"
	case GuildBridgeIfPortalExists:
		return "if-portal-exists"
	case GuildBridgeCreateOnMessage:
		return "create-on-message"
	case GuildBridgeEverything:
		return "everything"
	default:
		return ""
	}
}

func (gbm GuildBridgingMode) Description() string {
	switch gbm {
	case GuildBridgeNothing:
		return "never bridge messages"
	case GuildBridgeIfPortalExists:
		return "bridge messages in existing portals"
	case GuildBridgeCreateOnMessage:
		return "bridge all messages and create portals on first message"
	case GuildBridgeEverything:
		return "bridge all messages and create portals proactively"
	default:
		return ""
	}
}

type GuildQuery struct {
	db  *Database
	log log.Logger
}

const (
	guildSelect = "SELECT dcid, mxid, plain_name, name, name_set, avatar, avatar_url, avatar_set, bridging_mode FROM guild"
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
	PlainName string
	Name      string
	NameSet   bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	BridgingMode GuildBridgingMode
}

func (g *Guild) Scan(row dbutil.Scannable) *Guild {
	var mxid sql.NullString
	var avatarURL string
	err := row.Scan(&g.ID, &mxid, &g.PlainName, &g.Name, &g.NameSet, &g.Avatar, &avatarURL, &g.AvatarSet, &g.BridgingMode)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			g.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}
	if g.BridgingMode < GuildBridgeNothing || g.BridgingMode > GuildBridgeEverything {
		panic(fmt.Errorf("invalid guild bridging mode %d in guild %s", g.BridgingMode, g.ID))
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
		INSERT INTO guild (dcid, mxid, plain_name, name, name_set, avatar, avatar_url, avatar_set, bridging_mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := g.db.Exec(query, g.ID, g.mxidPtr(), g.PlainName, g.Name, g.NameSet, g.Avatar, g.AvatarURL.String(), g.AvatarSet, g.BridgingMode)
	if err != nil {
		g.log.Warnfln("Failed to insert %s: %v", g.ID, err)
		panic(err)
	}
}

func (g *Guild) Update() {
	query := `
		UPDATE guild SET mxid=$1, plain_name=$2, name=$3, name_set=$4, avatar=$5, avatar_url=$6, avatar_set=$7, bridging_mode=$8
		WHERE dcid=$9
	`
	_, err := g.db.Exec(query, g.mxidPtr(), g.PlainName, g.Name, g.NameSet, g.Avatar, g.AvatarURL.String(), g.AvatarSet, g.BridgingMode, g.ID)
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
