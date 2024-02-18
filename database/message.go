package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

const (
	messageSelect = "SELECT dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_edit_timestamp, dc_thread_id, mxid, sender_mxid FROM message"
)

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) scanAll(rows dbutil.Rows, err error) []*Message {
	if err != nil {
		mq.log.Warnfln("Failed to query many messages: %v", err)
		panic(err)
	} else if rows == nil {
		return nil
	}

	var messages []*Message
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}

	return messages
}

func (mq *MessageQuery) GetByDiscordID(key PortalKey, discordID string) []*Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3 ORDER BY dc_attachment_id ASC"
	return mq.scanAll(mq.db.Query(query, key.ChannelID, key.Receiver, discordID))
}

func (mq *MessageQuery) GetFirstByDiscordID(key PortalKey, discordID string) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3 ORDER BY dc_attachment_id ASC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, discordID))
}

func (mq *MessageQuery) GetLastByDiscordID(key PortalKey, discordID string) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3 ORDER BY dc_attachment_id DESC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, discordID))
}

func (mq *MessageQuery) GetClosestBefore(key PortalKey, threadID string, ts time.Time) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_thread_id=$3 AND timestamp<=$4 ORDER BY timestamp DESC, dc_attachment_id DESC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, threadID, ts.UnixMilli()))
}

func (mq *MessageQuery) GetLastInThread(key PortalKey, threadID string) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_thread_id=$3 ORDER BY timestamp DESC, dc_attachment_id DESC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver, threadID))
}

func (mq *MessageQuery) GetLast(key PortalKey) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 ORDER BY timestamp DESC LIMIT 1"
	return mq.New().Scan(mq.db.QueryRow(query, key.ChannelID, key.Receiver))
}

func (mq *MessageQuery) DeleteAll(key PortalKey) {
	query := "DELETE FROM message WHERE dc_chan_id=$1 AND dc_chan_receiver=$2"
	_, err := mq.db.Exec(query, key.ChannelID, key.Receiver)
	if err != nil {
		mq.log.Warnfln("Failed to delete messages of %s: %v", key, err)
		panic(err)
	}
}

func (mq *MessageQuery) GetByMXID(key PortalKey, mxid id.EventID) *Message {
	query := messageSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND mxid=$3"

	row := mq.db.QueryRow(query, key.ChannelID, key.Receiver, mxid)
	if row == nil {
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) MassInsert(key PortalKey, msgs []Message) {
	if len(msgs) == 0 {
		return
	}
	valueStringFormat := "($%d, $%d, $1, $2, $%d, $%d, $%d, $%d, $%d, $%d)"
	if mq.db.Dialect == dbutil.SQLite {
		valueStringFormat = strings.ReplaceAll(valueStringFormat, "$", "?")
	}
	params := make([]interface{}, 2+len(msgs)*8)
	placeholders := make([]string, len(msgs))
	params[0] = key.ChannelID
	params[1] = key.Receiver
	for i, msg := range msgs {
		baseIndex := 2 + i*8
		params[baseIndex] = msg.DiscordID
		params[baseIndex+1] = msg.AttachmentID
		params[baseIndex+2] = msg.SenderID
		params[baseIndex+3] = msg.Timestamp.UnixMilli()
		params[baseIndex+4] = msg.editTimestampVal()
		params[baseIndex+5] = msg.ThreadID
		params[baseIndex+6] = msg.MXID
		params[baseIndex+7] = msg.SenderMXID.String()
		placeholders[i] = fmt.Sprintf(valueStringFormat, baseIndex+1, baseIndex+2, baseIndex+3, baseIndex+4, baseIndex+5, baseIndex+6, baseIndex+7, baseIndex+8)
	}
	_, err := mq.db.Exec(fmt.Sprintf(messageMassInsertTemplate, strings.Join(placeholders, ", ")), params...)
	if err != nil {
		mq.log.Warnfln("Failed to insert %d messages: %v", len(msgs), err)
		panic(err)
	}
}

type Message struct {
	db  *Database
	log log.Logger

	DiscordID     string
	AttachmentID  string
	Channel       PortalKey
	SenderID      string
	Timestamp     time.Time
	EditTimestamp time.Time
	ThreadID      string

	MXID       id.EventID
	SenderMXID id.UserID
}

func (m *Message) DiscordProtoChannelID() string {
	if m.ThreadID != "" {
		return m.ThreadID
	} else {
		return m.Channel.ChannelID
	}
}

func (m *Message) Scan(row dbutil.Scannable) *Message {
	var ts, editTS int64

	err := row.Scan(&m.DiscordID, &m.AttachmentID, &m.Channel.ChannelID, &m.Channel.Receiver, &m.SenderID, &ts, &editTS, &m.ThreadID, &m.MXID, &m.SenderMXID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}

	if ts != 0 {
		m.Timestamp = time.UnixMilli(ts).UTC()
	}
	if editTS != 0 {
		m.EditTimestamp = time.Unix(0, editTS).UTC()
	}

	return m
}

const messageInsertQuery = `
	INSERT INTO message (
		dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_edit_timestamp, dc_thread_id, mxid, sender_mxid
	)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`

var messageMassInsertTemplate = strings.Replace(messageInsertQuery, "($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)", "%s", 1)

type MessagePart struct {
	AttachmentID string
	MXID         id.EventID
}

func (m *Message) editTimestampVal() int64 {
	if m.EditTimestamp.IsZero() {
		return 0
	}
	return m.EditTimestamp.UnixNano()
}

func (m *Message) MassInsertParts(msgs []MessagePart) {
	if len(msgs) == 0 {
		return
	}
	valueStringFormat := "($1, $%d, $2, $3, $4, $5, $6, $7, $%d, $8)"
	if m.db.Dialect == dbutil.SQLite {
		valueStringFormat = strings.ReplaceAll(valueStringFormat, "$", "?")
	}
	params := make([]interface{}, 8+len(msgs)*2)
	placeholders := make([]string, len(msgs))
	params[0] = m.DiscordID
	params[1] = m.Channel.ChannelID
	params[2] = m.Channel.Receiver
	params[3] = m.SenderID
	params[4] = m.Timestamp.UnixMilli()
	params[5] = m.editTimestampVal()
	params[6] = m.ThreadID
	params[7] = m.SenderMXID.String()
	for i, msg := range msgs {
		params[8+i*2] = msg.AttachmentID
		params[8+i*2+1] = msg.MXID
		placeholders[i] = fmt.Sprintf(valueStringFormat, 8+i*2+1, 8+i*2+2)
	}
	_, err := m.db.Exec(fmt.Sprintf(messageMassInsertTemplate, strings.Join(placeholders, ", ")), params...)
	if err != nil {
		m.log.Warnfln("Failed to insert %d parts of %s@%s: %v", len(msgs), m.DiscordID, m.Channel, err)
		panic(err)
	}
}

func (m *Message) Insert() {
	_, err := m.db.Exec(messageInsertQuery,
		m.DiscordID, m.AttachmentID, m.Channel.ChannelID, m.Channel.Receiver, m.SenderID,
		m.Timestamp.UnixMilli(), m.editTimestampVal(), m.ThreadID, m.MXID, m.SenderMXID.String())

	if err != nil {
		m.log.Warnfln("Failed to insert %s@%s: %v", m.DiscordID, m.Channel, err)
		panic(err)
	}
}

const editUpdateQuery = `
	UPDATE message
	SET dc_edit_timestamp=$1
	WHERE dcid=$2 AND dc_attachment_id=$3 AND dc_chan_id=$4 AND dc_chan_receiver=$5 AND dc_edit_timestamp<$1
`

func (m *Message) UpdateEditTimestamp(ts time.Time) {
	_, err := m.db.Exec(editUpdateQuery, ts.UnixNano(), m.DiscordID, m.AttachmentID, m.Channel.ChannelID, m.Channel.Receiver)
	if err != nil {
		m.log.Warnfln("Failed to update edit timestamp of %s@%s: %v", m.DiscordID, m.Channel, err)
		panic(err)
	}
}

func (m *Message) Delete() {
	query := "DELETE FROM message WHERE dcid=$1 AND dc_chan_id=$2 AND dc_chan_receiver=$3 AND dc_attachment_id=$4"
	_, err := m.db.Exec(query, m.DiscordID, m.Channel.ChannelID, m.Channel.Receiver, m.AttachmentID)
	if err != nil {
		m.log.Warnfln("Failed to delete %q of %s@%s: %v", m.AttachmentID, m.DiscordID, m.Channel, err)
		panic(err)
	}
}
