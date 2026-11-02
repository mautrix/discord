package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestLegacyGuildModeMigration(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
        CREATE TABLE user_old (mxid TEXT, dcid TEXT, discord_token TEXT, space_room TEXT, heartbeat_session TEXT);
        CREATE TABLE guild_old (dcid TEXT, bridging_mode INTEGER);
        CREATE TABLE user_portal_old (user_mxid TEXT, discord_id TEXT, type TEXT);
        CREATE TABLE user_login (bridge_id TEXT, user_mxid TEXT, id TEXT, remote_name TEXT, remote_profile TEXT, space_room TEXT, metadata TEXT);
        INSERT INTO user_old VALUES ('@a:test', '1', 'secret', '!space:test', NULL);
        INSERT INTO guild_old VALUES ('10', 1), ('20', 2), ('30', 3), ('40', 0);
        INSERT INTO user_portal_old VALUES ('@a:test', '10', 'guild'), ('@a:test', '20', 'guild'), ('@a:test', '30', 'guild'), ('@a:test', '40', 'guild');
    `)
	if err != nil {
		t.Fatal(err)
	}
	migration := legacyMigrateCopyData[strings.Index(legacyMigrateCopyData, "INSERT INTO user_login"):strings.Index(legacyMigrateCopyData, "INSERT INTO ghost")]
	for _, dialect := range []dbutil.Dialect{dbutil.SQLite, dbutil.Postgres} {
		wrapped := &dbutil.Database{Dialect: dialect}
		query, err := wrapped.Internals().FilterSQLUpgrade(bytes.Split([]byte(migration), []byte("\n")))
		if err != nil {
			t.Fatal(err)
		}
		if dialect == dbutil.Postgres {
			if strings.Contains(query, "json_object(") || !strings.Contains(query, "bg.bridging_mode") {
				t.Fatal("incorrect PostgreSQL migration")
			}
			continue
		}
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err = db.QueryRow("SELECT metadata FROM user_login").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta discordid.UserLoginMetadata
	if err = json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.GuildBridgingModes["10"] != discordid.GuildBridgeIfPortalExists || meta.GuildBridgingModes["20"] != discordid.GuildBridgeCreateOnMessage || meta.GuildBridgingModes["30"] != discordid.GuildBridgeEverything || meta.GuildBridgingModes["40"] != discordid.GuildBridgeNothing {
		t.Fatalf("modes changed: %+v", meta.GuildBridgingModes)
	}
	if meta.Token != "secret" {
		t.Fatal("login token lost")
	}
}
