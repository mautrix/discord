package migrations

import (
	"database/sql"
	"embed"

	"github.com/lopezator/migrator"
	log "maunium.net/go/maulogger/v2"
)

//go:embed *.sql
var migrations embed.FS

func migrationFromFile(filename string) *migrator.Migration {
	return &migrator.Migration{
		Name: filename,
		Func: func(tx *sql.Tx) error {
			data, err := migrations.ReadFile(filename)
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

func Run(db *sql.DB, baseLog log.Logger) error {
	subLogger := baseLog.Sub("Migrations")
	logger := migrator.LoggerFunc(func(msg string, args ...interface{}) {
		subLogger.Infof(msg, args...)
	})

	m, err := migrator.New(
		migrator.TableName("version"),
		migrator.WithLogger(logger),
		migrator.Migrations(
			migrationFromFile("01-initial.sql"),
			migrationFromFile("02-attachments.sql"),
			migrationFromFile("03-emoji.sql"),
			migrationFromFile("04-custom-puppet.sql"),
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
