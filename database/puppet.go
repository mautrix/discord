// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package database

import (
	"database/sql"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

const (
	puppetSelect = "SELECT id, name, name_set, avatar, avatar_url, avatar_set," +
		" contact_info_set, global_name, username, discriminator, is_bot, is_webhook, is_application, custom_mxid, access_token, next_batch" +
		" FROM puppet "
)

type PuppetQuery struct {
	qh *dbutil.QueryHelper[*Puppet]
}

func newPuppet(qh *dbutil.QueryHelper[*Puppet]) *Puppet {
	return &Puppet{qh: qh}
}

func (pq *PuppetQuery) Get(id string) *Puppet {
	return pq.get(puppetSelect+" WHERE id=$1", id)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	return pq.get(puppetSelect+" WHERE custom_mxid=$1", mxid)
}

func (pq *PuppetQuery) get(query string, args ...interface{}) *Puppet {
	return pq.New().Scan(pq.db.QueryRow(query, args...))
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

	var puppets []*Puppet
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}

	return puppets
}

type Puppet struct {
	qh *dbutil.QueryHelper[*Puppet]

	ID        string
	Name      string
	NameSet   bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	ContactInfoSet bool

	GlobalName    string
	Username      string
	Discriminator string
	IsBot         bool
	IsWebhook     bool
	IsApplication bool

	CustomMXID  id.UserID
	AccessToken string
	NextBatch   string
}

func (p *Puppet) Scan(row dbutil.Scannable) (*Puppet, error) {
	var avatarURL string
	var customMXID, accessToken, nextBatch sql.NullString
	err := row.Scan(&p.ID, &p.Name, &p.NameSet, &p.Avatar, &avatarURL, &p.AvatarSet, &p.ContactInfoSet,
		&p.GlobalName, &p.Username, &p.Discriminator, &p.IsBot, &p.IsWebhook, &p.IsApplication, &customMXID, &accessToken, &nextBatch)
	if err != nil {
		return nil, err
	}

	p.AvatarURL, _ = id.ParseContentURI(avatarURL)
	p.CustomMXID = id.UserID(customMXID.String)
	p.AccessToken = accessToken.String
	p.NextBatch = nextBatch.String
	return p, nil
}

func (p *Puppet) Insert() {
	query := `
		INSERT INTO puppet (
			id, name, name_set, avatar, avatar_url, avatar_set, contact_info_set,
			global_name, username, discriminator, is_bot, is_webhook, is_application,
			custom_mxid, access_token, next_batch
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`
	_, err := p.db.Exec(query, p.ID, p.Name, p.NameSet, p.Avatar, p.AvatarURL.String(), p.AvatarSet, p.ContactInfoSet,
		p.GlobalName, p.Username, p.Discriminator, p.IsBot, p.IsWebhook, p.IsApplication,
		strPtr(p.CustomMXID), strPtr(p.AccessToken), strPtr(p.NextBatch))

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.ID, err)
		panic(err)
	}
}

func (p *Puppet) Update() {
	query := `
		UPDATE puppet SET name=$1, name_set=$2, avatar=$3, avatar_url=$4, avatar_set=$5, contact_info_set=$6,
		                  global_name=$7, username=$8, discriminator=$9, is_bot=$10, is_webhook=$11, is_application=$12,
		                  custom_mxid=$13, access_token=$14, next_batch=$15
		WHERE id=$16
	`
	_, err := p.db.Exec(
		query,
		p.Name, p.NameSet, p.Avatar, p.AvatarURL.String(), p.AvatarSet, p.ContactInfoSet,
		p.GlobalName, p.Username, p.Discriminator, p.IsBot, p.IsWebhook, p.IsApplication,
		strPtr(p.CustomMXID), strPtr(p.AccessToken), strPtr(p.NextBatch),
		p.ID,
	)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.ID, err)
		panic(err)
	}
}
