package database

import (
	_ "embed"
	"fmt"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/mautrix-discord/database/upgrades"
)

type Database struct {
	*dbutil.Database

	User       *UserQuery
	Portal     *PortalQuery
	Puppet     *PuppetQuery
	Message    *MessageQuery
	Thread     *ThreadQuery
	Reaction   *ReactionQuery
	Attachment *AttachmentQuery
	Emoji      *EmojiQuery
	Guild      *GuildQuery
}

//go:embed legacymigrate.sql
var legacyMigrate string

func New(baseDB *dbutil.Database) *Database {
	db := &Database{Database: baseDB}
	_, err := db.Exec("SELECT id FROM version")
	if err == nil {
		baseDB.Log.Infoln("Migrating from legacy database versioning")
		_, err = db.Exec(legacyMigrate)
		if err != nil {
			panic(fmt.Errorf("failed to migrate from legacy database versioning: %v", err))
		}
	}
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
	db.Attachment = &AttachmentQuery{
		db:  db,
		log: db.Log.Sub("Attachment"),
	}
	db.Emoji = &EmojiQuery{
		db:  db,
		log: db.Log.Sub("Emoji"),
	}
	db.Guild = &GuildQuery{
		db:  db,
		log: db.Log.Sub("Guild"),
	}
	return db
}

func strPtr(val string) *string {
	if val == "" {
		return nil
	}
	return &val
}
