package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type EmojiQuery struct {
	db  *Database
	log log.Logger
}

const (
	emojiSelect = "SELECT discord_id, discord_name, matrix_url FROM emoji"
)

func (eq *EmojiQuery) New() *Emoji {
	return &Emoji{
		db:  eq.db,
		log: eq.log,
	}
}

func (eq *EmojiQuery) GetByDiscordID(discordID string) *Emoji {
	query := emojiSelect + " WHERE discord_id=$1"
	return eq.get(query, discordID)
}

func (eq *EmojiQuery) GetByMatrixURL(matrixURL id.ContentURI) *Emoji {
	query := emojiSelect + " WHERE matrix_url=$1"
	return eq.get(query, matrixURL.String())
}

func (eq *EmojiQuery) get(query string, args ...interface{}) *Emoji {
	return eq.New().Scan(eq.db.QueryRow(query, args...))
}

type Emoji struct {
	db  *Database
	log log.Logger

	DiscordID   string
	DiscordName string

	MatrixURL id.ContentURI
}

func (e *Emoji) Scan(row dbutil.Scannable) *Emoji {
	var matrixURL sql.NullString

	err := row.Scan(&e.DiscordID, &e.DiscordName, &matrixURL)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			e.log.Errorln("Database scan failed:", err)
			panic(err)
		}
		return nil
	}

	e.MatrixURL, _ = id.ParseContentURI(matrixURL.String)
	return e
}

func (e *Emoji) Insert() {
	query := "INSERT INTO emoji" +
		" (discord_id, discord_name, matrix_url)" +
		" VALUES ($1, $2, $3);"

	_, err := e.db.Exec(query, e.DiscordID, e.DiscordName, e.MatrixURL.String())

	if err != nil {
		e.log.Warnfln("Failed to insert emoji %s: %v", e.DiscordID, err)
		panic(err)
	}
}

func (e *Emoji) Delete() {
	query := "DELETE FROM emoji WHERE discord_id=$1"

	_, err := e.db.Exec(query, e.DiscordID)
	if err != nil {
		e.log.Warnfln("Failed to delete emoji %s: %v", e.DiscordID, err)
		panic(err)
	}
}

func (e *Emoji) APIName() string {
	if e.DiscordID != "" && e.DiscordName != "" {
		return e.DiscordName + ":" + e.DiscordID
	} else if e.DiscordName != "" {
		return e.DiscordName
	}
	return e.DiscordID
}
