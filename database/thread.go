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
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type ThreadQuery struct {
	*dbutil.QueryHelper[*Thread]
}

const (
	threadSelect = "SELECT dcid, parent_chan_id, root_msg_dcid, root_msg_mxid, creation_notice_mxid FROM thread"
)

func newThread(qh *dbutil.QueryHelper[*Thread]) *Thread {
	return &Thread{qh: qh}
}

func (tq *ThreadQuery) GetByDiscordID(discordID string) *Thread {
	query := threadSelect + " WHERE dcid=$1"

	row := tq.db.QueryRow(query, discordID)
	if row == nil {
		return nil
	}

	return tq.New().Scan(row)
}

func (tq *ThreadQuery) GetByMatrixRootMsg(mxid id.EventID) *Thread {
	query := threadSelect + " WHERE root_msg_mxid=$1"

	row := tq.db.QueryRow(query, mxid)
	if row == nil {
		return nil
	}

	return tq.New().Scan(row)
}

func (tq *ThreadQuery) GetByMatrixRootOrCreationNoticeMsg(mxid id.EventID) *Thread {
	query := threadSelect + " WHERE root_msg_mxid=$1 OR creation_notice_mxid=$1"

	row := tq.db.QueryRow(query, mxid)
	if row == nil {
		return nil
	}

	return tq.New().Scan(row)
}

type Thread struct {
	qh *dbutil.QueryHelper[*Thread]

	ID       string
	ParentID string

	RootDiscordID string
	RootMXID      id.EventID

	CreationNoticeMXID id.EventID
}

func (t *Thread) Scan(row dbutil.Scannable) (*Thread, error) {
	return dbutil.ValueOrErr(t, row.Scan(&t.ID, &t.ParentID, &t.RootDiscordID, &t.RootMXID, &t.CreationNoticeMXID))
}

func (t *Thread) Insert() {
	query := "INSERT INTO thread (dcid, parent_chan_id, root_msg_dcid, root_msg_mxid, creation_notice_mxid) VALUES ($1, $2, $3, $4, $5)"
	_, err := t.db.Exec(query, t.ID, t.ParentID, t.RootDiscordID, t.RootMXID, t.CreationNoticeMXID)
	if err != nil {
		t.log.Warnfln("Failed to insert %s@%s: %v", t.ID, t.ParentID, err)
		panic(err)
	}
}

func (t *Thread) Update() {
	query := "UPDATE thread SET creation_notice_mxid=$2 WHERE dcid=$1"
	_, err := t.db.Exec(query, t.ID, t.CreationNoticeMXID)
	if err != nil {
		t.log.Warnfln("Failed to update %s@%s: %v", t.ID, t.ParentID, err)
		panic(err)
	}
}

func (t *Thread) Delete() {
	query := "DELETE FROM thread WHERE dcid=$1 AND parent_chan_id=$2"
	_, err := t.db.Exec(query, t.ID, t.ParentID)
	if err != nil {
		t.log.Warnfln("Failed to delete %s@%s: %v", t.ID, t.ParentID, err)
		panic(err)
	}
}
