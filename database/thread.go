package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type ThreadQuery struct {
	db  *Database
	log log.Logger
}

const (
	threadSelect = "SELECT dcid, parent_chan_id, root_msg_dcid, root_msg_mxid, creation_notice_mxid FROM thread"
)

func (tq *ThreadQuery) New() *Thread {
	return &Thread{
		db:  tq.db,
		log: tq.log,
	}
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
	db  *Database
	log log.Logger

	ID       string
	ParentID string

	RootDiscordID string
	RootMXID      id.EventID

	CreationNoticeMXID id.EventID
}

func (t *Thread) Scan(row dbutil.Scannable) *Thread {
	err := row.Scan(&t.ID, &t.ParentID, &t.RootDiscordID, &t.RootMXID, &t.CreationNoticeMXID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			t.log.Errorln("Database scan failed:", err)
			panic(err)
		}
		return nil
	}
	return t
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
