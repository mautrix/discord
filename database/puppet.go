package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

const (
	puppetSelect = "SELECT id, display_name, avatar, avatar_url," +
		" custom_mxid, access_token, next_batch" +
		" FROM puppet "
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,
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

type Puppet struct {
	db  *Database
	log log.Logger

	ID          string
	DisplayName string

	Avatar    string
	AvatarURL id.ContentURI

	CustomMXID  id.UserID
	AccessToken string
	NextBatch   string
}

func (p *Puppet) Scan(row dbutil.Scannable) *Puppet {
	var did, displayName, avatar, avatarURL sql.NullString
	var customMXID, accessToken, nextBatch sql.NullString

	err := row.Scan(&did, &displayName, &avatar, &avatarURL,
		&customMXID, &accessToken, &nextBatch)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.ID = did.String
	p.DisplayName = displayName.String
	p.Avatar = avatar.String
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.CustomMXID = id.UserID(customMXID.String)
	p.AccessToken = accessToken.String
	p.NextBatch = nextBatch.String

	return p
}

func (p *Puppet) Insert() {
	query := "INSERT INTO puppet" +
		" (id, display_name, avatar, avatar_url," +
		"  custom_mxid, access_token, next_batch)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7)"

	_, err := p.db.Exec(query, p.ID, p.DisplayName, p.Avatar,
		p.AvatarURL.String(), p.CustomMXID, p.AccessToken,
		p.NextBatch)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.ID, err)
	}
}

func (p *Puppet) Update() {
	query := "UPDATE puppet" +
		" SET display_name=$1, avatar=$2, avatar_url=$3, " +
		"     custom_mxid=$4, access_token=$5, next_batch=$6" +
		" WHERE id=$7"

	_, err := p.db.Exec(query, p.DisplayName, p.Avatar, p.AvatarURL.String(),
		p.CustomMXID, p.AccessToken, p.NextBatch,
		p.ID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.ID, err)
	}
}
