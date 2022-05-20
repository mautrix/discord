package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
)

type SQLCryptoStore struct {
	*crypto.SQLCryptoStore
	UserID        id.UserID
	GhostIDFormat string
}

var _ crypto.Store = (*SQLCryptoStore)(nil)

func NewSQLCryptoStore(db *Database, userID id.UserID, ghostIDFormat string) *SQLCryptoStore {
	return &SQLCryptoStore{
		SQLCryptoStore: crypto.NewSQLCryptoStore(db.DB, db.dialect, "", "",
			[]byte("maunium.net/go/mautrix-whatsapp"),
			&cryptoLogger{db.log.Sub("CryptoStore")}),
		UserID:        userID,
		GhostIDFormat: ghostIDFormat,
	}
}

func (store *SQLCryptoStore) FindDeviceID() id.DeviceID {
	var deviceID id.DeviceID

	query := `SELECT device_id FROM crypto_account WHERE account_id=$1`
	err := store.DB.QueryRow(query, store.AccountID).Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		store.Log.Warn("Failed to scan device ID: %v", err)
	}

	return deviceID
}

func (store *SQLCryptoStore) GetRoomMembers(roomID id.RoomID) ([]id.UserID, error) {
	query := `
		SELECT user_id FROM mx_user_profile
		WHERE room_id=$1
			AND (membership='join' OR membership='invite')
			AND user_id<>$2
			AND user_id NOT LIKE $3
	`

	members := []id.UserID{}

	rows, err := store.DB.Query(query, roomID, store.UserID, store.GhostIDFormat)
	if err != nil {
		return members, err
	}

	for rows.Next() {
		var userID id.UserID
		err := rows.Scan(&userID)
		if err != nil {
			store.Log.Warn("Failed to scan member in %s: %v", roomID, err)
			return members, err
		}

		members = append(members, userID)
	}

	return members, nil
}

// TODO merge this with the one in the parent package
type cryptoLogger struct {
	int log.Logger
}

var levelTrace = log.Level{
	Name:     "TRACE",
	Severity: -10,
	Color:    -1,
}

func (c *cryptoLogger) Error(message string, args ...interface{}) {
	c.int.Errorfln(message, args...)
}

func (c *cryptoLogger) Warn(message string, args ...interface{}) {
	c.int.Warnfln(message, args...)
}

func (c *cryptoLogger) Debug(message string, args ...interface{}) {
	c.int.Debugfln(message, args...)
}

func (c *cryptoLogger) Trace(message string, args ...interface{}) {
	c.int.Logfln(levelTrace, message, args...)
}
