package database

import (
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type AttachmentQuery struct {
	db  *Database
	log log.Logger
}

const (
	attachmentSelect = "SELECT channel_id, receiver, discord_message_id," +
		" discord_attachment_id, matrix_event_id FROM attachment"
)

func (aq *AttachmentQuery) New() *Attachment {
	return &Attachment{
		db:  aq.db,
		log: aq.log,
	}
}

func (aq *AttachmentQuery) GetAllByDiscordMessageID(key PortalKey, discordMessageID string) []*Attachment {
	query := attachmentSelect + " WHERE channel_id=$1 AND receiver=$2 AND" +
		" discord_message_id=$3"

	return aq.getAll(query, key.ChannelID, key.Receiver, discordMessageID)
}

func (aq *AttachmentQuery) getAll(query string, args ...interface{}) []*Attachment {
	rows, err := aq.db.Query(query, args...)
	if err != nil {
		aq.log.Debugfln("getAll failed: %v", err)

		return nil
	}

	if rows == nil {
		return nil
	}

	attachments := []*Attachment{}
	for rows.Next() {
		attachments = append(attachments, aq.New().Scan(rows))
	}

	return attachments
}

func (aq *AttachmentQuery) GetByDiscordAttachmentID(key PortalKey, discordMessageID, discordID string) *Attachment {
	query := attachmentSelect + " WHERE channel_id=$1 AND receiver=$2" +
		" AND discord_message_id=$3 AND discord_id=$4"

	return aq.get(query, key.ChannelID, key.Receiver, discordMessageID, discordID)
}

func (aq *AttachmentQuery) GetByMatrixID(key PortalKey, matrixEventID id.EventID) *Attachment {
	query := attachmentSelect + " WHERE channel_id=$1 AND receiver=$2" +
		" AND matrix_event_id=$3"

	return aq.get(query, key.ChannelID, key.Receiver, matrixEventID)
}

func (aq *AttachmentQuery) get(query string, args ...interface{}) *Attachment {
	row := aq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return aq.New().Scan(row)
}
