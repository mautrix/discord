package config

import (
	log "maunium.net/go/maulogger/v2"

	db "go.mau.fi/mautrix-discord/database"
)

type database struct {
	Type string `yaml:"type"`
	URI  string `yaml:"uri"`

	MaxOpenConns int `yaml:"max_open_conns"`
	MaxIdleConns int `yaml:"max_idle_conns"`
}

func (d *database) validate() error {
	if d.Type == "" {
		d.Type = "sqlite3"
	}

	if d.URI == "" {
		d.URI = "mautrix-discord.db"
	}

	if d.MaxOpenConns == 0 {
		d.MaxOpenConns = 20
	}

	if d.MaxIdleConns == 0 {
		d.MaxIdleConns = 2
	}

	return nil
}

func (d *database) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawDatabase database

	raw := rawDatabase{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*d = database(raw)

	return d.validate()
}

func (c *Config) CreateDatabase(baseLog log.Logger) (*db.Database, error) {
	return db.New(
		c.Appservice.Database.Type,
		c.Appservice.Database.URI,
		c.Appservice.Database.MaxOpenConns,
		c.Appservice.Database.MaxIdleConns,
		baseLog,
	)
}
