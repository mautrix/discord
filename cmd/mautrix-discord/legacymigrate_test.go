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

func TestLegacyWebhookMigration(t *testing.T) {
	rawDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	_, err = rawDB.Exec(`
        CREATE TABLE user_old (mxid TEXT, dcid TEXT, discord_token TEXT);
        CREATE TABLE user_portal_old (user_mxid TEXT, discord_id TEXT, type TEXT);
        CREATE TABLE portal_old (dcid TEXT, receiver TEXT, mxid TEXT, dc_parent_id TEXT, dc_parent_receiver TEXT, dc_guild_id TEXT, other_user_id TEXT, name TEXT, topic TEXT, avatar TEXT, avatar_url TEXT, name_set INTEGER, avatar_set INTEGER, topic_set INTEGER, in_space TEXT, type INTEGER, relay_webhook_id TEXT, relay_webhook_secret TEXT);
        INSERT INTO user_old VALUES ('@listener:test', '100', 'token'), ('@loggedout:test', '50', ''), ('@unrelated:test', '1', 'other-token');
        INSERT INTO user_portal_old VALUES ('@listener:test', '300', 'guild'), ('@loggedout:test', '300', 'guild');
        INSERT INTO portal_old VALUES ('200', '', '!room:test', '', '', '300', '', 'channel', '', '', '', 1, 0, 0, '', 0, '400', 'secret');
        INSERT INTO portal_old SELECT '201', '', '!other:test', '', '', '300', '', 'other', '', '', '', 1, 0, 0, '', 0, NULL, NULL;
    `)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(legacyMigrateCopyData, "FROM guild_old;") + len("FROM guild_old;")
	migration := strings.TrimSpace(legacyMigrateCopyData[start:strings.Index(legacyMigrateCopyData, "INSERT INTO message (")])
	columns := strings.Split(migration[strings.Index(migration, "(")+1:strings.Index(migration, ")")], ",")
	for i := range columns {
		columns[i] = strings.TrimSpace(columns[i]) + " TEXT"
	}
	if _, err = rawDB.Exec("CREATE TABLE portal (" + strings.Join(columns, ",") + ")"); err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []dbutil.Dialect{dbutil.SQLite, dbutil.Postgres} {
		db := &dbutil.Database{Dialect: dialect}
		query, err := db.Internals().FilterSQLUpgrade(bytes.Split([]byte(migration), []byte("\n")))
		if err != nil {
			t.Fatal(err)
		}
		if dialect == dbutil.Postgres {
			if strings.Contains(query, "json_object(") || !strings.Contains(query, "'relay_webhook_secret'") {
				t.Fatal("incorrect PostgreSQL migration")
			}
		} else if _, err = rawDB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var relayBridge, relayLogin, raw string
	if err = rawDB.QueryRow("SELECT relay_bridge_id, relay_login_id, metadata FROM portal WHERE id='200'").Scan(&relayBridge, &relayLogin, &raw); err != nil {
		t.Fatal(err)
	}
	var meta discordid.PortalMetadata
	if err = json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	if relayBridge != "" || relayLogin != "100" || meta.RelayWebhookID != "400" || meta.RelayWebhookSecret != "secret" || meta.GuildID != "300" {
		t.Fatalf("relay not preserved: %s %s %+v", relayBridge, relayLogin, meta)
	}
	var count int
	if err = rawDB.QueryRow("SELECT count(*) FROM portal WHERE id='201' AND relay_login_id IS NULL AND relay_bridge_id IS NULL").Scan(&count); err != nil || count != 1 {
		t.Fatalf("non-relay portal changed: %d %v", count, err)
	}
}
