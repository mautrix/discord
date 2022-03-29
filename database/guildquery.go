package database

import (
	"fmt"

	log "maunium.net/go/maulogger/v2"
)

type GuildQuery struct {
	db  *Database
	log log.Logger
}

const (
	guildSelect = "SELECT discord_id, guild_id, guild_name, bridge FROM guild"
)

func (gq *GuildQuery) New() *Guild {
	return &Guild{
		db:  gq.db,
		log: gq.log,
	}
}

func (gq *GuildQuery) Get(discordID, guildID string) *Guild {
	query := guildSelect + " WHERE discord_id=$1 AND guild_id=$2"

	row := gq.db.QueryRow(query, discordID, guildID)
	if row == nil {
		return nil
	}

	return gq.New().Scan(row)
}

func (gq *GuildQuery) GetAll(discordID string) []*Guild {
	query := guildSelect + " WHERE discord_id=$1"

	rows, err := gq.db.Query(query, discordID)
	if err != nil || rows == nil {
		return nil
	}

	guilds := []*Guild{}
	for rows.Next() {
		guilds = append(guilds, gq.New().Scan(rows))
	}

	return guilds
}

func (gq *GuildQuery) Prune(discordID string, guilds []string) {
	// We need this interface slice because a variadic function can't mix
	// arguements with a `...` expanded slice.
	args := []interface{}{discordID}

	nGuilds := len(guilds)
	if nGuilds <= 0 {
		return
	}

	gq.log.Debugfln("prunning guilds not in %#v for %s", guilds, discordID)

	// Build the in query
	inQuery := "$2"
	for i := 1; i < nGuilds; i++ {
		inQuery += fmt.Sprintf(", $%d", i+2)
	}

	// Add the arguements for the build query
	for _, guildID := range guilds {
		args = append(args, guildID)
	}

	// Now remove any guilds that the user has left.
	query := "DELETE FROM guild WHERE discord_id=$1 AND guild_id NOT IN (" +
		inQuery + ")"

	_, err := gq.db.Exec(query, args...)
	if err != nil {
		gq.log.Warnfln("Failed to remove old guilds for user %s: %v", discordID, err)
	}
}
