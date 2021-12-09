package database

import (
	"database/sql"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	log "maunium.net/go/maulogger/v2"

	"gitlab.com/beeper/discord/database/migrations"
)

type Database struct {
	*sql.DB
	log     log.Logger
	dialect string
}

func New(dbType, uri string, maxOpenConns, maxIdleConns int, baseLog log.Logger) (*Database, error) {
	conn, err := sql.Open(dbType, uri)
	if err != nil {
		return nil, err
	}

	if dbType == "sqlite3" {
		conn.Exec("PRAGMA foreign_keys = ON")
	}

	conn.SetMaxOpenConns(maxOpenConns)
	conn.SetMaxIdleConns(maxIdleConns)

	dbLog := baseLog.Sub("Database")

	if err := migrations.Run(conn, dbLog); err != nil {
		return nil, err
	}

	db := &Database{
		DB:      conn,
		log:     dbLog,
		dialect: dbType,
	}

	return db, nil
}
