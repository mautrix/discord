package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type Puppet struct {
	db  *Database
	log log.Logger

	ID          string
	DisplayName string

	Avatar    string
	AvatarURL id.ContentURI

	EnablePresence bool
}

func (p *Puppet) Scan(row Scannable) *Puppet {
	var did, displayName, avatar, avatarURL sql.NullString
	var enablePresence sql.NullBool

	err := row.Scan(&did, &displayName, &avatar, &avatarURL, &enablePresence)
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
	p.EnablePresence = enablePresence.Bool

	return p
}

func (p *Puppet) Insert() {
	query := "INSERT INTO puppet" +
		" (id, display_name, avatar, avatar_url, enable_presence)" +
		" VALUES ($1, $2, $3, $4, $5)"

	_, err := p.db.Exec(query, p.ID, p.DisplayName, p.Avatar,
		p.AvatarURL.String(), p.EnablePresence)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.ID, err)
	}
}

func (p *Puppet) Update() {
	query := "UPDATE puppet" +
		" SET display_name=$1, avatar=$2, avatar_url=$3, enable_presence=$4" +
		" WHERE id=$5"

	_, err := p.db.Exec(query, p.DisplayName, p.Avatar, p.AvatarURL.String(),
		p.EnablePresence, p.ID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.ID, err)
	}
}
