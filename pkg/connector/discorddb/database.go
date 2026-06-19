// Package discorddb holds the connector-owned database tables for the Discord
// bridge (role/emoji/file/guild caches) that have no bridgev2 framework
// equivalent. It runs its own dbutil upgrade table on top of the shared
// *dbutil.Database used by the rest of the bridge.
package discorddb

import (
	"context"

	"go.mau.fi/util/dbutil"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb/upgrades"
)

// DiscordDB wraps the shared bridge database and owns the Discord-specific
// tables (dc_guild, dc_role, dc_emoji, dc_file).
type DiscordDB struct {
	*dbutil.Database

	Role  *RoleQuery
	File  *FileQuery
	Emoji *EmojiQuery
	Guild *GuildQuery
}

// New wraps an existing *dbutil.Database with a DiscordDB that uses the
// connector-owned upgrade table. The returned DiscordDB owns the four dc_*
// query helpers; callers must call Upgrade before issuing any queries.
func New(db *dbutil.Database) *DiscordDB {
	db = db.Child("discord_version", upgrades.Table, nil)
	return &DiscordDB{
		Database: db,
		Role:     &RoleQuery{QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*Role]) *Role { return &Role{} })},
		File:     &FileQuery{QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*File]) *File { return &File{} })},
		Emoji:    &EmojiQuery{QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*Emoji]) *Emoji { return &Emoji{} })},
		Guild:    &GuildQuery{QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*Guild]) *Guild { return &Guild{} })},
	}
}

// Upgrade runs the connector-owned upgrade table, creating/migrating the
// dc_* tables. It is safe to run repeatedly (no-op when already at latest).
func (db *DiscordDB) Upgrade(ctx context.Context) error {
	return db.Database.Upgrade(ctx)
}
