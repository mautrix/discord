package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const (
	puppetSelect = "SELECT id, display_name, avatar, avatar_url," +
		" enable_presence, custom_mxid, access_token, next_batch," +
		" enable_receipts" +
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

	CustomMXID  id.UserID
	AccessToken string

	NextBatch string

	EnableReceipts bool
}

func (p *Puppet) Scan(row Scannable) *Puppet {
	var did, displayName, avatar, avatarURL sql.NullString
	var enablePresence sql.NullBool
	var customMXID, accessToken, nextBatch sql.NullString

	err := row.Scan(&did, &displayName, &avatar, &avatarURL, &enablePresence,
		&customMXID, &accessToken, &nextBatch, &p.EnableReceipts)

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
	p.CustomMXID = id.UserID(customMXID.String)
	p.AccessToken = accessToken.String
	p.NextBatch = nextBatch.String

	return p
}

func (p *Puppet) Insert() {
	query := "INSERT INTO puppet" +
		" (id, display_name, avatar, avatar_url, enable_presence," +
		"  custom_mxid, access_token, next_batch, enable_receipts)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)"

	_, err := p.db.Exec(query, p.ID, p.DisplayName, p.Avatar,
		p.AvatarURL.String(), p.EnablePresence, p.CustomMXID, p.AccessToken,
		p.NextBatch, p.EnableReceipts)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.ID, err)
	}
}

func (p *Puppet) Update() {
	query := "UPDATE puppet" +
		" SET display_name=$1, avatar=$2, avatar_url=$3, enable_presence=$4," +
		"     custom_mxid=$5, access_token=$6, next_batch=$7," +
		"     enable_receipts=$8" +
		" WHERE id=$9"

	_, err := p.db.Exec(query, p.DisplayName, p.Avatar, p.AvatarURL.String(),
		p.EnablePresence, p.CustomMXID, p.AccessToken, p.NextBatch,
		p.EnableReceipts, p.ID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.ID, err)
	}
}
