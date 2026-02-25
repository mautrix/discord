// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package discorddb

import (
	"context"
	"database/sql"

	"go.mau.fi/util/dbutil"
)

type ThreadQuery struct {
	*dbutil.QueryHelper[*Thread]
}

type Thread struct {
	UserLoginID     string
	ThreadChannelID string
	RootMessageID   string
	ParentChannelID string
}

func (t *Thread) sqlVariables() []any {
	var rootMsgID *string
	if t.RootMessageID != "" {
		rootMsgID = &t.RootMessageID
	}
	return []any{
		t.UserLoginID,
		t.ThreadChannelID,
		rootMsgID,
		t.ParentChannelID,
	}
}

func newThread(_ *dbutil.QueryHelper[*Thread]) *Thread {
	return &Thread{}
}

const (
	getThreadByChannelIDQuery = `
		SELECT user_login_id, thread_channel_id, root_message_id, parent_channel_id
		FROM discord_thread
		WHERE user_login_id=$1 AND thread_channel_id=$2
	`
	getThreadByRootMessageIDQuery = `
		SELECT user_login_id, thread_channel_id, root_message_id, parent_channel_id
		FROM discord_thread
		WHERE user_login_id=$1 AND root_message_id=$2
	`
	upsertThreadQuery = `
		INSERT INTO discord_thread (user_login_id, thread_channel_id, root_message_id, parent_channel_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_login_id, thread_channel_id) DO UPDATE
			SET root_message_id = excluded.root_message_id,
			    parent_channel_id = excluded.parent_channel_id
	`
	deleteThreadByChannelIDQuery = `
		DELETE FROM discord_thread WHERE user_login_id=$1 AND thread_channel_id=$2
	`
)

func (tq *ThreadQuery) GetByThreadChannelID(ctx context.Context, userLoginID, threadChannelID string) (*Thread, error) {
	return tq.QueryOne(ctx, getThreadByChannelIDQuery, &userLoginID, &threadChannelID)
}

func (tq *ThreadQuery) GetByRootMessageID(ctx context.Context, userLoginID, rootMessageID string) (*Thread, error) {
	return tq.QueryOne(ctx, getThreadByRootMessageIDQuery, &userLoginID, &rootMessageID)
}

func (tq *ThreadQuery) Put(ctx context.Context, thread *Thread) error {
	return tq.Exec(ctx, upsertThreadQuery, thread.sqlVariables()...)
}

func (tq *ThreadQuery) DeleteByThreadChannelID(ctx context.Context, userLoginID, threadChannelID string) error {
	return tq.Exec(ctx, deleteThreadByChannelIDQuery, &userLoginID, &threadChannelID)
}

func (t *Thread) Scan(row dbutil.Scannable) (*Thread, error) {
	var rootMsgID sql.NullString
	err := row.Scan(
		&t.UserLoginID,
		&t.ThreadChannelID,
		&rootMsgID,
		&t.ParentChannelID,
	)
	if err != nil {
		return nil, err
	}
	t.RootMessageID = rootMsgID.String
	return t, nil
}
