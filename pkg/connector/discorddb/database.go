// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package discorddb

import (
	"embed"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
)

type DiscordDB struct {
	*dbutil.Database
	CustomEmoji *CustomEmojiQuery
	Role        *RoleQuery
}

var table dbutil.UpgradeTable

//go:embed *.sql
var upgrades embed.FS

func init() {
	table.RegisterFS(upgrades)
}

func New(db *dbutil.Database, log zerolog.Logger) *DiscordDB {
	db = db.Child("discord_version", table, dbutil.ZeroLogger(log))
	return &DiscordDB{
		Database: db,
		CustomEmoji: &CustomEmojiQuery{
			QueryHelper: dbutil.MakeQueryHelper(db, newCustomEmoji),
		},
		Role: &RoleQuery{
			QueryHelper: dbutil.MakeQueryHelper(db, newRole),
		},
	}
}
