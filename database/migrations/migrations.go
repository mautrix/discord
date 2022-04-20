package migrations

import (
	"database/sql"
	"embed"

	"github.com/lopezator/migrator"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

//go:embed *.sql
var embeddedMigrations embed.FS

func migrationFromFile(description, filename string) *migrator.Migration {
	return &migrator.Migration{
		Name: description,
		Func: func(tx *sql.Tx) error {
			data, err := embeddedMigrations.ReadFile(filename)
			if err != nil {
				return err
			}

			if _, err := tx.Exec(string(data)); err != nil {
				return err
			}

			return nil
		},
	}
}

func migrationFromFileWithDialect(dialect, description, sqliteFile, postgresFile string) *migrator.Migration {
	switch dialect {
	case "sqlite3":
		return migrationFromFile(description, sqliteFile)
	case "postgres":
		return migrationFromFile(description, postgresFile)
	default:
		return nil
	}
}

func Run(db *sql.DB, baseLog log.Logger, dialect string) error {
	subLogger := baseLog.Sub("Migrations")
	logger := migrator.LoggerFunc(func(msg string, args ...interface{}) {
		subLogger.Infof(msg, args...)
	})

	m, err := migrator.New(
		migrator.TableName("version"),
		migrator.WithLogger(logger),
		migrator.Migrations(
			migrationFromFile("Initial Schema", "01-initial.sql"),
			migrationFromFile("Attachments", "02-attachments.sql"),
			migrationFromFile("Emoji", "03-emoji.sql"),
			migrationFromFile("Custom Puppets", "04-custom-puppet.sql"),
			migrationFromFile(
				"Additional puppet fields",
				"05-additional-puppet-fields.sql",
			),
			migrationFromFileWithDialect(
				dialect,
				"Remove unique user constraint",
				"06-remove-unique-user-constraint.sqlite.sql",
				"06-remove-unique-user-constraint.postgres.sql",
			),
			migrationFromFile("Guild Bridging", "07-guilds.sql"),
			&migrator.Migration{
				Name: "Add crypto store to database",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[0](tx, dialect)
				},
			},
			&migrator.Migration{
				Name: "Add account_id to crypto store",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[1](tx, dialect)
				},
			},
			&migrator.Migration{
				Name: "Add megolm withheld data to crypto store",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[2](tx, dialect)
				},
			},
			&migrator.Migration{
				Name: "Add cross-signing keys to crypto store",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[3](tx, dialect)
				},
			},
			&migrator.Migration{
				Name: "Replace VARCHAR(255) with TEXT in the crypto database",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[4](tx, dialect)
				},
			},
			&migrator.Migration{
				Name: "Split last_used into last_encrypted and last_decrypted in crypto store",
				Func: func(tx *sql.Tx) error {
					return sql_store_upgrade.Upgrades[5](tx, dialect)
				},
			},
			migrationFromFile(
				"Add encryption column to portal table",
				"14-add-encrypted-column-to-portal-table.sql",
			),
		),
	)
	if err != nil {
		return err
	}

	if err := m.Migrate(db); err != nil {
		return err
	}

	return nil
}
