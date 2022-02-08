package database

import (
	"database/sql"
	"errors"
	"time"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type Message struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	DiscordID string
	MatrixID  id.EventID

	AuthorID  string
	Timestamp time.Time
}

func (m *Message) Scan(row Scannable) *Message {
	var ts int64

	err := row.Scan(&m.Channel.ChannelID, &m.Channel.Receiver, &m.DiscordID, &m.MatrixID, &m.AuthorID, &ts)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if ts != 0 {
		m.Timestamp = time.Unix(ts, 0)
	}

	return m
}

func (m *Message) Insert() {
	query := "INSERT INTO message" +
		" (channel_id, receiver, discord_message_id, matrix_message_id," +
		" author_id, timestamp) VALUES ($1, $2, $3, $4, $5, $6)"

	_, err := m.db.Exec(query, m.Channel.ChannelID, m.Channel.Receiver,
		m.DiscordID, m.MatrixID, m.AuthorID, m.Timestamp.Unix())

	if err != nil {
		m.log.Warnfln("Failed to insert %s@%s: %v", m.Channel, m.DiscordID, err)
	}
}

func (m *Message) Delete() {
	query := "DELETE FROM message" +
		" WHERE channel_id=$1 AND receiver=$2 AND discord_message_id=$3 AND" +
		" matrix_message_id=$4"

	_, err := m.db.Exec(query, m.Channel.ChannelID, m.Channel.Receiver,
		m.DiscordID, m.MatrixID)

	if err != nil {
		m.log.Warnfln("Failed to delete %s@%s: %v", m.Channel, m.DiscordID, err)
	}
}

func (m *Message) UpdateMatrixID(mxid id.EventID) {
	query := "UPDATE message SET matrix_message_id=$1 WHERE channel_id=$2" +
		"AND receiver=$3 AND discord_message_id=$4"
	m.MatrixID = mxid

	_, err := m.db.Exec(query, m.MatrixID, m.Channel.ChannelID, m.Channel.Receiver, m.DiscordID)
	if err != nil {
		m.log.Warnfln("Failed to update %s@%s: %v", m.Channel, m.DiscordID, err)
	}
}
