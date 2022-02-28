package migrations

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"

	"github.com/lopezator/migrator"
	log "maunium.net/go/maulogger/v2"
)

//go:embed *.sql
var embeddedMigrations embed.FS

var (
	commonMigrations = []string{
		"01-initial.sql",
		"02-attachments.sql",
		"03-emoji.sql",
		"04-custom-puppet.sql",
		"05-additional-puppet-fields.sql",
	}

	sqliteMigrations = []string{
		"06-remove-unique-user-constraint.sqlite.sql",
	}

	postgresMigrations = []string{
		"06-remove-unique-user-constraint.postgres.sql",
	}
)

func migrationFromFile(filename string) *migrator.Migration {
	return &migrator.Migration{
		Name: filename,
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

func Run(db *sql.DB, baseLog log.Logger, dialect string) error {
	subLogger := baseLog.Sub("Migrations")
	logger := migrator.LoggerFunc(func(msg string, args ...interface{}) {
		subLogger.Infof(msg, args...)
	})

	migrationNames := commonMigrations
	switch dialect {
	case "sqlite3":
		migrationNames = append(migrationNames, sqliteMigrations...)
	case "postgres":
		migrationNames = append(migrationNames, postgresMigrations...)
	}

	sort.Strings(migrationNames)

	migrations := make([]interface{}, len(migrationNames))
	for idx, name := range migrationNames {
		fmt.Printf("migration: %s\n", name)
		migrations[idx] = migrationFromFile(name)
	}

	fmt.Printf("migrations(%d)\n", len(migrations))

	m, err := migrator.New(
		migrator.TableName("version"),
		migrator.WithLogger(logger),
		migrator.Migrations(migrations...),
	)
	if err != nil {
		return err
	}

	if err := m.Migrate(db); err != nil {
		return err
	}

	return nil
}
