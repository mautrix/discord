// mautrix-discord - A Matrix-Discord puppeting bridge.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
	_ "embed"
)

// legacyMigrateRenameTables renames the 10 legacy (mautrix-discord v0.x / schema
// v24) tables to *_old so the bridgev2 framework can create its own tables with
// the original names, then legacyMigrateCopyData transforms the data across.
//
// IMPORTANT: this MUST NOT reference or rename any crypto_* table. The crypto
// store (crypto_account, crypto_megolm_inbound_session, crypto_olm_session, ...)
// is shared verbatim between the legacy and bridgev2 layouts, so it is left in
// place untouched. Renaming or dropping it would destroy all Olm/Megolm sessions
// and make every encrypted room undecryptable (NFR-11).
const legacyMigrateRenameTables = `
ALTER TABLE portal RENAME TO portal_old;
ALTER TABLE puppet RENAME TO puppet_old;
ALTER TABLE "user" RENAME TO user_old;
ALTER TABLE user_portal RENAME TO user_portal_old;
ALTER TABLE message RENAME TO message_old;
ALTER TABLE reaction RENAME TO reaction_old;
ALTER TABLE guild RENAME TO guild_old;
ALTER TABLE thread RENAME TO thread_old;
ALTER TABLE role RENAME TO role_old;
ALTER TABLE discord_file RENAME TO discord_file_old;
`

//go:embed legacymigrate.sql
var legacyMigrateCopyData string
