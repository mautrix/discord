package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type ReactionQuery struct {
	db  *Database
	log log.Logger
}

const (
	reactionSelect = "SELECT dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name, dc_thread_id, mxid FROM reaction"
)

func (rq *ReactionQuery) New() *Reaction {
	return &Reaction{
		db:  rq.db,
		log: rq.log,
	}
}

func (rq *ReactionQuery) GetAllForMessage(key PortalKey, discordMessageID string) []*Reaction {
	query := reactionSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_msg_id=$3"

	return rq.getAll(query, key.ChannelID, key.Receiver, discordMessageID)
}

func (rq *ReactionQuery) getAll(query string, args ...interface{}) []*Reaction {
	rows, err := rq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}

	var reactions []*Reaction
	for rows.Next() {
		reactions = append(reactions, rq.New().Scan(rows))
	}

	return reactions
}

func (rq *ReactionQuery) GetByDiscordID(key PortalKey, msgID, sender, emojiName string) *Reaction {
	query := reactionSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_msg_id=$3 AND dc_sender=$4 AND dc_emoji_name=$5"

	return rq.get(query, key.ChannelID, key.Receiver, msgID, sender, emojiName)
}

func (rq *ReactionQuery) GetByMXID(mxid id.EventID) *Reaction {
	query := reactionSelect + " WHERE mxid=$1"

	return rq.get(query, mxid)
}

func (rq *ReactionQuery) get(query string, args ...interface{}) *Reaction {
	row := rq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return rq.New().Scan(row)
}

type Reaction struct {
	db  *Database
	log log.Logger

	Channel   PortalKey
	MessageID string
	Sender    string
	EmojiName string
	ThreadID  string

	MXID id.EventID
}

func (r *Reaction) Scan(row dbutil.Scannable) *Reaction {
	err := row.Scan(&r.Channel.ChannelID, &r.Channel.Receiver, &r.MessageID, &r.Sender, &r.EmojiName, &r.ThreadID, &r.MXID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.log.Errorln("Database scan failed:", err)
			panic(err)
		}
		return nil
	}

	return r
}

func (r *Reaction) DiscordProtoChannelID() string {
	if r.ThreadID != "" {
		return r.ThreadID
	} else {
		return r.Channel.ChannelID
	}
}

func (r *Reaction) Insert() {
	query := `
		INSERT INTO reaction (dc_msg_id, dc_sender, dc_emoji_name, dc_chan_id, dc_chan_receiver, dc_thread_id, mxid)
		VALUES($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.Exec(query, r.MessageID, r.Sender, r.EmojiName, r.Channel.ChannelID, r.Channel.Receiver, strPtr(r.ThreadID), r.MXID)
	if err != nil {
		r.log.Warnfln("Failed to insert reaction for %s@%s: %v", r.MessageID, r.Channel, err)
		panic(err)
	}
}

func (r *Reaction) Delete() {
	query := "DELETE FROM reaction WHERE dc_msg_id=$1 AND dc_sender=$2 AND dc_emoji_name=$3"
	_, err := r.db.Exec(query, r.MessageID, r.Sender, r.EmojiName)
	if err != nil {
		r.log.Warnfln("Failed to delete reaction for %s@%s: %v", r.MessageID, r.Channel, err)
		panic(err)
	}
}
