package database

import (
	"database/sql"

	"github.com/bwmarrin/discordgo"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

// language=postgresql
const (
	portalSelect = `
		SELECT dcid, receiver, type, other_user_id, dc_guild_id, dc_parent_id, mxid,
		       plain_name, name, name_set, friend_nick, topic, topic_set, avatar, avatar_url, avatar_set,
		       encrypted, in_space, first_event_id, relay_webhook_id, relay_webhook_secret
		FROM portal
	`
)

type PortalKey struct {
	ChannelID string
	Receiver  string
}

func NewPortalKey(channelID, receiver string) PortalKey {
	return PortalKey{
		ChannelID: channelID,
		Receiver:  receiver,
	}
}

func (key PortalKey) String() string {
	if key.Receiver == "" {
		return key.ChannelID
	}
	return key.ChannelID + "-" + key.Receiver
}

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

func (pq *PortalQuery) GetAllInGuild(guildID string) []*Portal {
	return pq.getAll(portalSelect+" WHERE dc_guild_id=$1", guildID)
}

func (pq *PortalQuery) GetByID(key PortalKey) *Portal {
	return pq.get(portalSelect+" WHERE dcid=$1 AND (receiver=$2 OR receiver='')", key.ChannelID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(portalSelect+" WHERE mxid=$1", mxid)
}

func (pq *PortalQuery) FindPrivateChatBetween(id, receiver string) *Portal {
	return pq.get(portalSelect+" WHERE other_user_id=$1 AND receiver=$2 AND type=$3", id, receiver, discordgo.ChannelTypeDM)
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
	return pq.New().Scan(pq.db.QueryRow(query, args...))
}

type Portal struct {
	db  *Database
	log log.Logger

	Key         PortalKey
	Type        discordgo.ChannelType
	OtherUserID string
	ParentID    string
	GuildID     string

	MXID id.RoomID

	PlainName  string
	Name       string
	NameSet    bool
	FriendNick bool
	Topic      string
	TopicSet   bool
	Avatar     string
	AvatarURL  id.ContentURI
	AvatarSet  bool
	Encrypted  bool
	InSpace    id.RoomID

	FirstEventID id.EventID

	RelayWebhookID     string
	RelayWebhookSecret string
}

func (p *Portal) Scan(row dbutil.Scannable) *Portal {
	var otherUserID, guildID, parentID, mxid, firstEventID, relayWebhookID, relayWebhookSecret sql.NullString
	var chanType int32
	var avatarURL string

	err := row.Scan(&p.Key.ChannelID, &p.Key.Receiver, &chanType, &otherUserID, &guildID, &parentID,
		&mxid, &p.PlainName, &p.Name, &p.NameSet, &p.FriendNick, &p.Topic, &p.TopicSet, &p.Avatar, &avatarURL, &p.AvatarSet,
		&p.Encrypted, &p.InSpace, &firstEventID, &relayWebhookID, &relayWebhookSecret)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.OtherUserID = otherUserID.String
	p.GuildID = guildID.String
	p.ParentID = parentID.String
	p.Type = discordgo.ChannelType(chanType)
	p.FirstEventID = id.EventID(firstEventID.String)
	p.AvatarURL, _ = id.ParseContentURI(avatarURL)
	p.RelayWebhookID = relayWebhookID.String
	p.RelayWebhookSecret = relayWebhookSecret.String

	return p
}

func (p *Portal) Insert() {
	query := `
		INSERT INTO portal (dcid, receiver, type, other_user_id, dc_guild_id, dc_parent_id, mxid,
		                    plain_name, name, name_set, friend_nick, topic, topic_set, avatar, avatar_url, avatar_set,
		                    encrypted, in_space, first_event_id, relay_webhook_id, relay_webhook_secret)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
	`
	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver, p.Type,
		strPtr(p.OtherUserID), strPtr(p.GuildID), strPtr(p.ParentID), strPtr(string(p.MXID)),
		p.PlainName, p.Name, p.NameSet, p.FriendNick, p.Topic, p.TopicSet, p.Avatar, p.AvatarURL.String(), p.AvatarSet,
		p.Encrypted, p.InSpace, p.FirstEventID.String(), strPtr(p.RelayWebhookID), strPtr(p.RelayWebhookSecret))

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
		panic(err)
	}
}

func (p *Portal) Update() {
	query := `
		UPDATE portal
		SET type=$1, other_user_id=$2, dc_guild_id=$3, dc_parent_id=$4, mxid=$5,
			plain_name=$6, name=$7, name_set=$8, friend_nick=$9, topic=$10, topic_set=$11,
			avatar=$12, avatar_url=$13, avatar_set=$14, encrypted=$15, in_space=$16, first_event_id=$17,
			relay_webhook_id=$18, relay_webhook_secret=$19
		WHERE dcid=$20 AND receiver=$21
	`
	_, err := p.db.Exec(query,
		p.Type, strPtr(p.OtherUserID), strPtr(p.GuildID), strPtr(p.ParentID), strPtr(string(p.MXID)),
		p.PlainName, p.Name, p.NameSet, p.FriendNick, p.Topic, p.TopicSet,
		p.Avatar, p.AvatarURL.String(), p.AvatarSet, p.Encrypted, p.InSpace, p.FirstEventID.String(),
		strPtr(p.RelayWebhookID), strPtr(p.RelayWebhookSecret),
		p.Key.ChannelID, p.Key.Receiver)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
		panic(err)
	}
}

func (p *Portal) Delete() {
	query := "DELETE FROM portal WHERE dcid=$1 AND receiver=$2"
	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver)
	if err != nil {
		p.log.Warnfln("Failed to delete %s: %v", p.Key, err)
		panic(err)
	}
}
