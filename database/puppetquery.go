package database

import (
	log "maunium.net/go/maulogger/v2"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,

		EnablePresence: true,
	}
}

func (pq *PuppetQuery) Get(id string) *Puppet {
	row := pq.db.QueryRow("SELECT id, displayname, avatar, avatar_url, enable_presence FROM puppet WHERE id=$1", id)
	if row == nil {
		return nil
	}

	return pq.New().Scan(row)
}
