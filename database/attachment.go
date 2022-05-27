package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type AttachmentQuery struct {
	db  *Database
	log log.Logger
}

const (
	attachmentSelect = "SELECT dcid, dc_msg_id, dc_chan_id, dc_chan_receiver FROM attachment"
)

func (aq *AttachmentQuery) New() *Attachment {
	return &Attachment{
		db:  aq.db,
		log: aq.log,
	}
}

func (aq *AttachmentQuery) GetAllByDiscordMessageID(key PortalKey, discordMessageID string) []*Attachment {
	query := attachmentSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_msg_id=$3"

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

	var attachments []*Attachment
	for rows.Next() {
		attachments = append(attachments, aq.New().Scan(rows))
	}

	return attachments
}

func (aq *AttachmentQuery) GetByDiscordID(key PortalKey, discordMessageID, discordID string) *Attachment {
	query := attachmentSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_msg_id=$3 AND dcid=$4"

	return aq.get(query, key.ChannelID, key.Receiver, discordMessageID, discordID)
}

func (aq *AttachmentQuery) GetByMatrixID(key PortalKey, matrixEventID id.EventID) *Attachment {
	query := attachmentSelect + " WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND mxid=$3"

	return aq.get(query, key.ChannelID, key.Receiver, matrixEventID)
}

func (aq *AttachmentQuery) get(query string, args ...interface{}) *Attachment {
	row := aq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return aq.New().Scan(row)
}

type Attachment struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	DiscordMessageID    string
	DiscordAttachmentID string
	MXID                id.EventID
}

func (a *Attachment) Scan(row dbutil.Scannable) *Attachment {
	err := row.Scan(
		&a.DiscordAttachmentID, &a.DiscordMessageID,
		&a.Channel.ChannelID, &a.Channel.Receiver,
		&a.MXID)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	return a
}

func (a *Attachment) Insert() {
	query := "INSERT INTO attachment" +
		" (dcid, dc_msg_id, dc_chan_id, dc_chan_receiver, " +
		" mxid) VALUES ($1, $2, $3, $4, $5);"

	_, err := a.db.Exec(
		query,
		a.Channel.ChannelID, a.Channel.Receiver,
		a.DiscordMessageID, a.DiscordAttachmentID,
		a.MXID,
	)

	if err != nil {
		a.log.Warnfln("Failed to insert attachment for %s@%s: %v", a.DiscordAttachmentID, a.Channel, err)
	}
}

func (a *Attachment) Delete() {
	query := "DELETE FROM attachment WHERE" +
		" dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3"

	_, err := a.db.Exec(
		query,
		a.Channel.ChannelID, a.Channel.Receiver,
		a.DiscordAttachmentID,
	)

	if err != nil {
		a.log.Warnfln("Failed to delete attachment for %s@%s: %v", a.DiscordAttachmentID, a.Channel, err)
	}
}
