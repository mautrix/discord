package database

import (
	"database/sql"
	"errors"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

const (
	messageSelect = "SELECT dcid, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid FROM message"
)

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(key PortalKey) []*Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2"

	rows, err := mq.db.Query(query, key.ChannelID, key.Receiver)
	if err != nil || rows == nil {
		return nil
	}

	var messages []*Message
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}

	return messages
}

func (mq *MessageQuery) GetByDiscordID(key PortalKey, discordID string) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, discordID))
}

func (mq *MessageQuery) GetLastInThread(key PortalKey, threadID string) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_thread_id=$3 ORDER BY timestamp DESC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, threadID))
}

func (mq *MessageQuery) GetByMXID(key PortalKey, mxid id.EventID) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND mxid=$3"

	row := mq.db.QueryRow(query, key.ChannelID, key.Receiver, mxid)
	if row == nil {
		return nil
	}

	return mq.New().Scan(row)
}

type Message struct {
	db  *Database
	log log.Logger

	DiscordID string
	Channel   PortalKey
	SenderID  string
	Timestamp time.Time
	ThreadID  string

	MXID id.EventID
}

func (m *Message) DiscordProtoChannelID() string {
	if m.ThreadID != "" {
		return m.ThreadID
	} else {
		return m.Channel.ChannelID
	}
}

func (m *Message) Scan(row dbutil.Scannable) *Message {
	var ts int64
	var threadID sql.NullString

	err := row.Scan(&m.DiscordID, &m.Channel.ChannelID, &m.Channel.Receiver, &m.SenderID, &ts, &threadID, &m.MXID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}

	if ts != 0 {
		m.Timestamp = time.UnixMilli(ts)
	}
	m.ThreadID = threadID.String

	return m
}

func (m *Message) Insert() {
	query := `
		INSERT INTO message (dcid, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := m.db.Exec(query,
		m.DiscordID, m.Channel.ChannelID, m.Channel.Receiver, m.SenderID,
		m.Timestamp.UnixMilli(), strPtr(m.ThreadID), m.MXID)

	if err != nil {
		m.log.Warnfln("Failed to insert %s@%s: %v", m.DiscordID, m.Channel, err)
		panic(err)
	}
}

func (m *Message) Delete() {
	query := "DELETE FROM message WHERE dcid=$1 AND dc_chan_id=$2 AND dc_chan_receiver=$3"

	_, err := m.db.Exec(query, m.DiscordID, m.Channel.ChannelID, m.Channel.Receiver)

	if err != nil {
		m.log.Warnfln("Failed to delete %s@%s: %v", m.DiscordID, m.Channel, err)
		panic(err)
	}
}
