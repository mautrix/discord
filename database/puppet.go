package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const (
	puppetSelect = "SELECT id, display_name, avatar, avatar_url, " +
		" enable_presence, custom_mxid, access_token" +
		" FROM puppet "
)

type Puppet struct {
	db  *Database
	log log.Logger

	ID          string
	DisplayName string

	Avatar    string
	AvatarURL id.ContentURI

	EnablePresence bool

	CustomMXID  string
	AccessToken string
}

func (p *Puppet) Scan(row Scannable) *Puppet {
	var did, displayName, avatar, avatarURL sql.NullString
	var enablePresence sql.NullBool
	var customMXID, accessToken sql.NullString

	err := row.Scan(&did, &displayName, &avatar, &avatarURL, &enablePresence, &customMXID, &accessToken)
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
	p.CustomMXID = customMXID.String
	p.AccessToken = accessToken.String

	return p
}

func (p *Puppet) Insert() {
	query := "INSERT INTO puppet" +
		" (id, display_name, avatar, avatar_url, enable_presence," +
		"  custom_mxid, access_token)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7)"

	_, err := p.db.Exec(query, p.ID, p.DisplayName, p.Avatar,
		p.AvatarURL.String(), p.EnablePresence, p.CustomMXID, p.AccessToken)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.ID, err)
	}
}

func (p *Puppet) Update() {
	query := "UPDATE puppet" +
		" SET display_name=$1, avatar=$2, avatar_url=$3, enable_presence=$4" +
		"     custom_mxid=$5, access_token=$6" +
		" WHERE id=$7"

	_, err := p.db.Exec(query, p.DisplayName, p.Avatar, p.AvatarURL.String(),
		p.EnablePresence, p.CustomMXID, p.AccessToken, p.ID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.ID, err)
	}
}
