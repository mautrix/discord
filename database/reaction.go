package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type Reaction struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	DiscordMessageID string
	MatrixEventID    id.EventID

	// The discord ID of who create this reaction
	AuthorID string

	MatrixName string
	MatrixURL  string // Used for custom emoji

	DiscordID string // The id or unicode of the emoji for discord
}

func (r *Reaction) Scan(row Scannable) *Reaction {
	var discordID sql.NullString

	err := row.Scan(
		&r.Channel.ChannelID, &r.Channel.Receiver,
		&r.DiscordMessageID, &r.MatrixEventID,
		&r.AuthorID,
		&r.MatrixName, &r.MatrixURL,
		&discordID)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	r.DiscordID = discordID.String

	return r
}

func (r *Reaction) Insert() {
	query := "INSERT INTO reaction" +
		" (channel_id, receiver, discord_message_id, matrix_event_id," +
		"  author_id, matrix_name, matrix_url, discord_id)" +
		" VALUES($1, $2, $3, $4, $5, $6, $7, $8);"

	var discordID sql.NullString

	if r.DiscordID != "" {
		discordID = sql.NullString{r.DiscordID, true}
	}

	_, err := r.db.Exec(
		query,
		r.Channel.ChannelID, r.Channel.Receiver,
		r.DiscordMessageID, r.MatrixEventID,
		r.AuthorID,
		r.MatrixName, r.MatrixURL,
		discordID,
	)

	if err != nil {
		r.log.Warnfln("Failed to insert reaction for %s@%s: %v", r.Channel, r.DiscordMessageID, err)
	}
}

func (r *Reaction) Update() {
	// TODO: determine if we need this. The only scenario I can think of that
	// would require this is if we insert a custom emoji before uploading to
	// the homeserver?
}

func (r *Reaction) Delete() {
	query := "DELETE FROM reaction WHERE" +
		" channel_id=$1 AND receiver=$2 AND discord_message_id=$3 AND" +
		" author_id=$4 AND discord_id=$5"

	var discordID sql.NullString
	if r.DiscordID != "" {
		discordID = sql.NullString{r.DiscordID, true}
	}

	_, err := r.db.Exec(
		query,
		r.Channel.ChannelID, r.Channel.Receiver,
		r.DiscordMessageID, r.AuthorID,
		discordID,
	)

	if err != nil {
		r.log.Warnfln("Failed to delete reaction for %s@%s: %v", r.Channel, r.DiscordMessageID, err)
	}
}
