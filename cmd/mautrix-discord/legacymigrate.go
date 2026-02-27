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

package main

import (
	_ "embed"
)

const legacyMigrateRenameTables = `
ALTER TABLE portal RENAME TO portal_old;
ALTER TABLE puppet RENAME TO puppet_old;
ALTER TABLE "user" RENAME TO user_old;
ALTER TABLE message RENAME TO message_old;
ALTER TABLE reaction RENAME TO reaction_old;
ALTER TABLE user_portal RENAME TO user_portal_old;
ALTER TABLE guild RENAME TO guild_old;
ALTER TABLE role RENAME TO role_old;
ALTER TABLE thread RENAME TO thread_old;
ALTER TABLE discord_file RENAME TO discord_file_old;
`

//go:embed legacymigrate.sql
var legacyMigrateCopyData string
