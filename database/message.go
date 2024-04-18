// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type MessageQuery struct {
	*dbutil.QueryHelper[*Message]
}

const (
	getMessageBaseQuery = `
		SELECT dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender, timestamp,
		       dc_edit_timestamp, dc_thread_id, mxid, sender_mxid
		FROM message
	`
	getMessageByDiscordIDQuery = getMessageBaseQuery +
		"WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dcid=$3 ORDER BY dc_attachment_id"
	getFirstMessageByDiscordIDQuery = getMessageByDiscordIDQuery +
		" LIMIT 1"
	getLastMessageByDiscordIDQuery = getMessageByDiscordIDQuery +
		" DESC LIMIT 1"
	getClosestMessageBeforeTimeQuery = getMessageBaseQuery +
		"WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_thread_id=$3 AND timestamp<=$4 ORDER BY timestamp DESC, dc_attachment_id DESC LIMIT 1"
	getLastMessageInThreadQuery = getMessageBaseQuery +
		" WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_thread_id=$3 ORDER BY timestamp DESC, dc_attachment_id DESC LIMIT 1"
	getLastMessageInPortalQuery = getMessageBaseQuery +
		" WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 ORDER BY timestamp DESC LIMIT 1"
	getMessageByMXIDQuery = getMessageBaseQuery +
		" WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND mxid=$3"
	deleteAllMessagesInPortalQuery = "DELETE FROM message WHERE dc_chan_id=$1 AND dc_chan_receiver=$2"
)

func newMessage(qh *dbutil.QueryHelper[*Message]) *Message {
	return &Message{qh: qh}
}

func (mq *MessageQuery) GetByDiscordID(ctx context.Context, key PortalKey, discordID string) ([]*Message, error) {
	return mq.QueryMany(ctx, getMessageByDiscordIDQuery, key.ChannelID, key.Receiver, discordID)
}

func (mq *MessageQuery) GetFirstByDiscordID(ctx context.Context, key PortalKey, discordID string) (*Message, error) {
	return mq.QueryOne(ctx, getFirstMessageByDiscordIDQuery, key.ChannelID, key.Receiver, discordID)
}

func (mq *MessageQuery) GetLastByDiscordID(ctx context.Context, key PortalKey, discordID string) (*Message, error) {
	return mq.QueryOne(ctx, getLastMessageByDiscordIDQuery, key.ChannelID, key.Receiver, discordID)
}

func (mq *MessageQuery) GetClosestBefore(ctx context.Context, key PortalKey, threadID string, ts time.Time) (*Message, error) {
	return mq.QueryOne(ctx, getClosestMessageBeforeTimeQuery, key.ChannelID, key.Receiver, threadID, ts.UnixMilli())
}

func (mq *MessageQuery) GetLastInThread(ctx context.Context, key PortalKey, threadID string) (*Message, error) {
	return mq.QueryOne(ctx, getLastMessageInThreadQuery, key.ChannelID, key.Receiver, threadID)
}

func (mq *MessageQuery) GetLast(ctx context.Context, key PortalKey) (*Message, error) {
	return mq.QueryOne(ctx, getLastMessageInPortalQuery, key.ChannelID, key.Receiver)
}

func (mq *MessageQuery) GetByMXID(ctx context.Context, key PortalKey, mxid id.EventID) (*Message, error) {
	return mq.QueryOne(ctx, getMessageByMXIDQuery, key.ChannelID, key.Receiver, mxid)
}

func (mq *MessageQuery) DeleteAll(ctx context.Context, key PortalKey) error {
	return mq.Exec(ctx, deleteAllMessagesInPortalQuery, key.ChannelID, key.Receiver)
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
	qh *dbutil.QueryHelper[*Message]

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

func (m *Message) Scan(row dbutil.Scannable) (*Message, error) {
	var ts, editTS int64
	err := row.Scan(&m.DiscordID, &m.AttachmentID, &m.Channel.ChannelID, &m.Channel.Receiver, &m.SenderID, &ts, &editTS, &m.ThreadID, &m.MXID, &m.SenderMXID)
	if err != nil {
		return nil, err
	}

	if ts != 0 {
		m.Timestamp = time.UnixMilli(ts).UTC()
	}
	if editTS != 0 {
		m.EditTimestamp = time.Unix(0, editTS).UTC()
	}

	return m, nil
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
