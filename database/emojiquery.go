package database

import (
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
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
	row := eq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return eq.New().Scan(row)
}
