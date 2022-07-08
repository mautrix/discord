package database

import (
	_ "embed"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/mautrix-discord/database/upgrades"
)

type Database struct {
	*dbutil.Database

	User     *UserQuery
	Portal   *PortalQuery
	Puppet   *PuppetQuery
	Message  *MessageQuery
	Thread   *ThreadQuery
	Reaction *ReactionQuery
	Emoji    *EmojiQuery
	Guild    *GuildQuery
	Role     *RoleQuery
}

func New(baseDB *dbutil.Database) *Database {
	db := &Database{Database: baseDB}
	db.UpgradeTable = upgrades.Table
	db.User = &UserQuery{
		db:  db,
		log: db.Log.Sub("User"),
	}
	db.Portal = &PortalQuery{
		db:  db,
		log: db.Log.Sub("Portal"),
	}
	db.Puppet = &PuppetQuery{
		db:  db,
		log: db.Log.Sub("Puppet"),
	}
	db.Message = &MessageQuery{
		db:  db,
		log: db.Log.Sub("Message"),
	}
	db.Thread = &ThreadQuery{
		db:  db,
		log: db.Log.Sub("Thread"),
	}
	db.Reaction = &ReactionQuery{
		db:  db,
		log: db.Log.Sub("Reaction"),
	}
	db.Emoji = &EmojiQuery{
		db:  db,
		log: db.Log.Sub("Emoji"),
	}
	db.Guild = &GuildQuery{
		db:  db,
		log: db.Log.Sub("Guild"),
	}
	db.Role = &RoleQuery{
		db:  db,
		log: db.Log.Sub("Role"),
	}
	return db
}

func strPtr(val string) *string {
	if val == "" {
		return nil
	}
	return &val
}
