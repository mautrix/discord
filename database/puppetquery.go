package database

import (
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
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
	return pq.get(puppetSelect+" WHERE id=$1", id)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	return pq.get(puppetSelect+" WHERE custom_mxid=$1", mxid)
}

func (pq *PuppetQuery) get(query string, args ...interface{}) *Puppet {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetAll() []*Puppet {
	return pq.getAll(puppetSelect)
}

func (pq *PuppetQuery) GetAllWithCustomMXID() []*Puppet {
	return pq.getAll(puppetSelect + " WHERE custom_mxid<>''")
}

func (pq *PuppetQuery) getAll(query string, args ...interface{}) []*Puppet {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	puppets := []*Puppet{}
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}

	return puppets
}
