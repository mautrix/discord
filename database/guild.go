package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/util/dbutil"
)

type Guild struct {
	db  *Database
	log log.Logger

	DiscordID string
	GuildID   string
	GuildName string
	Bridge    bool
}

func (g *Guild) Scan(row dbutil.Scannable) *Guild {
	err := row.Scan(&g.DiscordID, &g.GuildID, &g.GuildName, &g.Bridge)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			g.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	return g
}

func (g *Guild) Upsert() {
	query := "INSERT INTO guild" +
		" (discord_id, guild_id, guild_name, bridge)" +
		" VALUES ($1, $2, $3, $4)" +
		" ON CONFLICT(discord_id, guild_id)" +
		" DO UPDATE SET guild_name=excluded.guild_name, bridge=excluded.bridge"

	_, err := g.db.Exec(query, g.DiscordID, g.GuildID, g.GuildName, g.Bridge)

	if err != nil {
		g.log.Warnfln("Failed to upsert guild %s for %s: %v", g.GuildID, g.DiscordID, err)
	}
}

func (g *Guild) Delete() {
	query := "DELETE FROM guild WHERE discord_id=$1 AND guild_id=$2"

	_, err := g.db.Exec(query, g.DiscordID, g.GuildID)

	if err != nil {
		g.log.Warnfln("Failed to delete guild %s for user %s: %v", g.GuildID, g.DiscordID, err)
	}
}
