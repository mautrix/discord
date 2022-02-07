package database

import (
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

const (
	messageSelect = "SELECT channel_id, receiver, discord_message_id," +
		" matrix_message_id, author_id, timestamp FROM message"
)

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(key PortalKey) []*Message {
	query := messageSelect + " WHERE channeld_id=$1 AND receiver=$2"

	rows, err := mq.db.Query(query, key.ChannelID, key.Receiver)
	if err != nil || rows == nil {
		return nil
	}

	messages := []*Message{}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}

	return messages
}

func (mq *MessageQuery) GetByDiscordID(key PortalKey, discordID string) *Message {
	query := messageSelect + " WHERE channel_id=$1 AND receiver=$2 AND" +
		" discord_message_id=$3"

	row := mq.db.QueryRow(query, key.ChannelID, key.Receiver, discordID)
	if row == nil {
		mq.log.Debugfln("failed to find existing message for discord_id %s", discordID)
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) GetByMatrixID(key PortalKey, matrixID id.EventID) *Message {
	query := messageSelect + " WHERE channel_id=$1 AND receiver=$2 AND" +
		" matrix_message_id=$3"

	row := mq.db.QueryRow(query, key.ChannelID, key.Receiver, matrixID)
	if row == nil {
		return nil
	}

	return mq.New().Scan(row)
}
