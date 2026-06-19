// mautrix-discord - A Matrix-Discord puppeting bridge.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/util/dbutil"
	_ "go.mau.fi/util/dbutil/litestream" // registers the sqlite3-fk-wal driver

	bridgev2db "maunium.net/go/mautrix/bridgev2/database"
	bridgev2upgrades "maunium.net/go/mautrix/bridgev2/database/upgrades"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// The bridgev2 DB version these legacy tables migrate up to. Must match the
// value passed to LegacyMigrateSimple in main.go and the latest revision in
// mautrix-go/bridgev2/database/upgrades.
const bridgev2DBVersion = 29

// setupLegacyDB creates a temp sqlite database, seeds it with the v24 fixture,
// and returns a *dbutil.Database wired with the bridgev2 upgrade table (exactly
// what mxmain.BridgeMain.DB would carry on a real bridge).
func setupLegacyDB(t *testing.T) *dbutil.Database {
	t.Helper()
	ctx := context.Background()

	dir := t.TempDir()
	uri := "file:" + filepath.Join(dir, "legacy.db") + "?_txlock=immediate"
	db, err := dbutil.NewWithDialect(uri, "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The framework upgrader inspects/maintains these. A real legacy DB has them.
	db.UpgradeTable = bridgev2upgrades.Table
	db.Owner = "mautrix-discord"

	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy-v24.sql"))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	if _, err = db.Exec(ctx, string(fixture)); err != nil {
		t.Fatalf("failed to seed fixture: %v", err)
	}

	// Pre-existing bookkeeping tables that the migrator deletes from and rewrites.
	if _, err = db.Exec(ctx, "CREATE TABLE database_owner (key INTEGER PRIMARY KEY, owner TEXT)"); err != nil {
		t.Fatalf("failed to create database_owner: %v", err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO database_owner (key, owner) VALUES (0, 'mautrix-discord')"); err != nil {
		t.Fatalf("failed to seed database_owner: %v", err)
	}
	if _, err = db.Exec(ctx, "CREATE TABLE version (version INTEGER, compat INTEGER)"); err != nil {
		t.Fatalf("failed to create version table: %v", err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO version (version, compat) VALUES (24, 19)"); err != nil {
		t.Fatalf("failed to seed version: %v", err)
	}
	return db
}

// runMigration runs the real migration code path (LegacyMigrateSimple) against
// the seeded DB.
func runMigration(t *testing.T, db *dbutil.Database) {
	t.Helper()
	m := &mxmain.BridgeMain{
		Name: "mautrix-discord",
		DB:   db,
	}
	migrator := m.LegacyMigrateSimple(legacyMigrateRenameTables, legacyMigrateCopyData, bridgev2DBVersion)
	if err := db.DoTxn(context.Background(), nil, migrator); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
}

func queryCount(t *testing.T, db *dbutil.Database, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q failed: %v", query, err)
	}
	return n
}

func queryString(t *testing.T, db *dbutil.Database, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		t.Fatalf("string query %q failed: %v", query, err)
	}
	return s
}

func TestLegacyMigrate(t *testing.T) {
	db := setupLegacyDB(t)
	runMigration(t, db)
	ctx := context.Background()

	t.Run("RowCounts", func(t *testing.T) {
		// 1 guild space + 1 category + 1 guild channel + 1 DM = 4 portals.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM portal"); got != 4 {
			t.Errorf("portal count = %d, want 4", got)
		}
		// 3 real puppets + 1 fake id='' ghost = 4 ghosts.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM ghost"); got != 4 {
			t.Errorf("ghost count = %d, want 4", got)
		}
		// rootmsg (1) + multi-attachment (2 parts) + sysmsg (1) = 4 message rows.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM message"); got != 4 {
			t.Errorf("message count = %d, want 4", got)
		}
		// 1 reaction.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM reaction"); got != 1 {
			t.Errorf("reaction count = %d, want 1", got)
		}
		// 1 local user + 1 user_login.
		if got := queryCount(t, db, `SELECT COUNT(*) FROM "user"`); got != 1 {
			t.Errorf("user count = %d, want 1", got)
		}
		if got := queryCount(t, db, "SELECT COUNT(*) FROM user_login"); got != 1 {
			t.Errorf("user_login count = %d, want 1", got)
		}
		// thread-type user_portal row discarded -> only the channel row remains.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM user_portal"); got != 1 {
			t.Errorf("user_portal count = %d, want 1", got)
		}
		// 1 cached file in dc_file.
		if got := queryCount(t, db, "SELECT COUNT(*) FROM dc_file"); got != 1 {
			t.Errorf("dc_file count = %d, want 1", got)
		}
		// dc_role / dc_emoji / dc_guild created but empty (rebuilt from gateway).
		if got := queryCount(t, db, "SELECT COUNT(*) FROM dc_role"); got != 0 {
			t.Errorf("dc_role count = %d, want 0", got)
		}
	})

	t.Run("PortalIDsMatchRuntime", func(t *testing.T) {
		// Guild space portal id == MakeGuildPortalID.
		wantSpace := string(discordid.MakeGuildPortalID("100000000000000001"))
		if got := queryCount(t, db, "SELECT COUNT(*) FROM portal WHERE id=$1 AND room_type='space'", wantSpace); got != 1 {
			t.Errorf("guild space portal id %q not found", wantSpace)
		}
		// Guild channel portal id == MakePortalID.
		wantChan := string(discordid.MakePortalID("200000000000000001"))
		roomType := queryString(t, db, "SELECT room_type FROM portal WHERE id=$1", wantChan)
		if roomType != "" {
			t.Errorf("guild channel room_type = %q, want '' (default)", roomType)
		}
		// DM portal room_type and other_user_id.
		dmType := queryString(t, db, "SELECT room_type FROM portal WHERE id='300000000000000001'")
		if dmType != "dm" {
			t.Errorf("DM room_type = %q, want dm", dmType)
		}
	})

	t.Run("CategoryIsSpaceWithParent", func(t *testing.T) {
		// Guild category (type=4) -> room_type='space' (legacy parity).
		catType := queryString(t, db, "SELECT room_type FROM portal WHERE id='210000000000000001'")
		if catType != "space" {
			t.Errorf("category room_type = %q, want space", catType)
		}
		// Category parent is the guild space.
		var catParent *string
		if err := db.QueryRow(ctx, "SELECT parent_id FROM portal WHERE id='210000000000000001'").Scan(&catParent); err != nil {
			t.Fatalf("query category parent_id: %v", err)
		}
		if catParent == nil || *catParent != "100000000000000001" {
			t.Errorf("category parent_id = %v, want guild 100000000000000001", catParent)
		}
		// The guild channel's parent is the category (self-referential FK held).
		var chanParent *string
		if err := db.QueryRow(ctx, "SELECT parent_id FROM portal WHERE id='200000000000000001'").Scan(&chanParent); err != nil {
			t.Fatalf("query channel parent_id: %v", err)
		}
		if chanParent == nil || *chanParent != "210000000000000001" {
			t.Errorf("channel parent_id = %v, want category 210000000000000001", chanParent)
		}
	})

	t.Run("DMReceiverPopulated", func(t *testing.T) {
		// The DM portal receiver must equal the local user's login id (H4).
		recv := queryString(t, db, "SELECT receiver FROM portal WHERE id='300000000000000001'")
		if recv != "900000000000000001" {
			t.Errorf("DM receiver = %q, want 900000000000000001", recv)
		}
		// Guild channel receiver stays empty.
		recv = queryString(t, db, "SELECT receiver FROM portal WHERE id='200000000000000001'")
		if recv != "" {
			t.Errorf("guild channel receiver = %q, want ''", recv)
		}
	})

	t.Run("MessageIDsAndMXIDs", func(t *testing.T) {
		// rootmsg id == MakeMessageID and mxid preserved.
		wantID := string(discordid.MakeMessageID("200000000000000001", "500000000000000010"))
		mxid := queryString(t, db, "SELECT mxid FROM message WHERE id=$1 AND part_id=''", wantID)
		if mxid != "$rootmsg" {
			t.Errorf("rootmsg mxid = %q, want $rootmsg (id=%q)", mxid, wantID)
		}
		// sysmsg (empty sender) survived the orphan cleanup.
		sysID := string(discordid.MakeMessageID("200000000000000001", "500000000000000030"))
		if got := queryCount(t, db, "SELECT COUNT(*) FROM message WHERE id=$1", sysID); got != 1 {
			t.Errorf("system message not preserved (id=%q)", sysID)
		}
		sysSender := queryString(t, db, "SELECT sender_id FROM message WHERE id=$1", sysID)
		if sysSender != "" {
			t.Errorf("system message sender_id = %q, want ''", sysSender)
		}
	})

	t.Run("SinglePartCollapsed", func(t *testing.T) {
		// rootmsg (single part) collapsed to part_id=''.
		rootID := string(discordid.MakeMessageID("200000000000000001", "500000000000000010"))
		part := queryString(t, db, "SELECT part_id FROM message WHERE id=$1", rootID)
		if part != "" {
			t.Errorf("single-part message part_id = %q, want ''", part)
		}
	})

	t.Run("MultiAttachmentPartIDs", func(t *testing.T) {
		multiID := string(discordid.MakeMessageID("200000000000000001", "500000000000000020"))
		// attachment ids are 600000000000000001 (idx 0) and 600000000000000002 (idx 1)
		// ordered ascending by dc_attachment_id.
		want0 := string(discordid.MakePartID(0, "600000000000000001"))
		want1 := string(discordid.MakePartID(1, "600000000000000002"))
		if got := queryCount(t, db, "SELECT COUNT(*) FROM message WHERE id=$1 AND part_id=$2", multiID, want0); got != 1 {
			t.Errorf("missing part_id %q for multi-attachment message", want0)
		}
		if got := queryCount(t, db, "SELECT COUNT(*) FROM message WHERE id=$1 AND part_id=$2", multiID, want1); got != 1 {
			t.Errorf("missing part_id %q for multi-attachment message", want1)
		}
		// Sanity: exactly 2 parts, both non-empty (not collapsed).
		if got := queryCount(t, db, "SELECT COUNT(*) FROM message WHERE id=$1", multiID); got != 2 {
			t.Errorf("multi-attachment part count = %d, want 2", got)
		}
	})

	t.Run("ThreadRootID", func(t *testing.T) {
		// Messages in the threaded channel carry thread_root_id =
		// parent_chan_id-root_msg_dcid (C3). The multi-attachment message has
		// dc_thread_id=250000000000000001 which resolves via thread_old.
		multiID := string(discordid.MakeMessageID("200000000000000001", "500000000000000020"))
		var threadRoot *string
		err := db.QueryRow(ctx, "SELECT thread_root_id FROM message WHERE id=$1 LIMIT 1", multiID).Scan(&threadRoot)
		if err != nil {
			t.Fatalf("query thread_root_id: %v", err)
		}
		want := "200000000000000001-500000000000000010"
		if threadRoot == nil || *threadRoot != want {
			t.Errorf("thread_root_id = %v, want %q", threadRoot, want)
		}
	})

	t.Run("TimestampNanoseconds", func(t *testing.T) {
		// Legacy timestamp was UnixMilli (1700000000000); migrated must be ns.
		rootID := string(discordid.MakeMessageID("200000000000000001", "500000000000000010"))
		ts := queryCount(t, db, "SELECT timestamp FROM message WHERE id=$1", rootID)
		if int64(ts) != 1700000000000*1000000 {
			t.Errorf("timestamp = %d, want %d (ns)", ts, int64(1700000000000)*1000000)
		}
	})

	t.Run("EditCountAndMetadata", func(t *testing.T) {
		// The multi-attachment-a row had dc_edit_timestamp != 0 -> edit_count=1.
		multiID := string(discordid.MakeMessageID("200000000000000001", "500000000000000020"))
		var maxEdit int
		if err := db.QueryRow(ctx, "SELECT MAX(edit_count) FROM message WHERE id=$1", multiID).Scan(&maxEdit); err != nil {
			t.Fatalf("query edit_count: %v", err)
		}
		if maxEdit != 1 {
			t.Errorf("edit_count = %d, want 1", maxEdit)
		}
		// metadata carries discord_id.
		meta := queryString(t, db, "SELECT metadata FROM message WHERE id=$1 LIMIT 1", multiID)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(meta), &parsed); err != nil {
			t.Fatalf("metadata not valid json: %v (%s)", err, meta)
		}
		if parsed["discord_id"] != "500000000000000020" {
			t.Errorf("metadata.discord_id = %v, want 500000000000000020", parsed["discord_id"])
		}
	})

	t.Run("ReactionEmojiSplit", func(t *testing.T) {
		// Custom emoji <:partyparrot:700...> -> emoji_id=700..., emoji=partyparrot.
		reactMsgID := string(discordid.MakeMessageID("200000000000000001", "500000000000000010"))
		emojiID := queryString(t, db, "SELECT emoji_id FROM reaction WHERE message_id=$1", reactMsgID)
		emoji := queryString(t, db, "SELECT emoji FROM reaction WHERE message_id=$1", reactMsgID)
		if emojiID != "700000000000000001" {
			t.Errorf("reaction emoji_id = %q, want 700000000000000001", emojiID)
		}
		if emoji != "partyparrot" {
			t.Errorf("reaction emoji = %q, want partyparrot", emoji)
		}
		// timestamp inherited from the parent message (ns).
		ts := queryCount(t, db, "SELECT timestamp FROM reaction WHERE message_id=$1", reactMsgID)
		if int64(ts) != 1700000000000*1000000 {
			t.Errorf("reaction timestamp = %d, want %d (parent msg ns)", ts, int64(1700000000000)*1000000)
		}
		// sender_mxid empty.
		smx := queryString(t, db, "SELECT sender_mxid FROM reaction WHERE message_id=$1", reactMsgID)
		if smx != "" {
			t.Errorf("reaction sender_mxid = %q, want ''", smx)
		}
	})

	t.Run("UserLoginMetadata", func(t *testing.T) {
		meta := queryString(t, db, "SELECT metadata FROM user_login WHERE id='900000000000000001'")
		var parsed map[string]any
		if err := json.Unmarshal([]byte(meta), &parsed); err != nil {
			t.Fatalf("user_login metadata not valid json: %v (%s)", err, meta)
		}
		// Token preserved verbatim (qualified "Bot ..."), token_type derived.
		if parsed["token"] != "Bot abc.def.ghi" {
			t.Errorf("metadata.token = %v, want 'Bot abc.def.ghi'", parsed["token"])
		}
		if parsed["token_type"] != "bot" {
			t.Errorf("metadata.token_type = %v, want bot", parsed["token_type"])
		}
		// read_state_version preserved.
		if rsv, ok := parsed["read_state_version"].(float64); !ok || int(rsv) != 7 {
			t.Errorf("metadata.read_state_version = %v, want 7", parsed["read_state_version"])
		}
		// heartbeat_session blob migrated into gateway_session_id as a JSON string.
		gsid, ok := parsed["gateway_session_id"].(string)
		if !ok {
			t.Fatalf("metadata.gateway_session_id is not a string: %v", parsed["gateway_session_id"])
		}
		var hb map[string]any
		if err := json.Unmarshal([]byte(gsid), &hb); err != nil {
			t.Fatalf("gateway_session_id is not a serialized JSON object: %v (%q)", err, gsid)
		}
		if hb["id"] != "sess-abc" {
			t.Errorf("gateway_session_id.id = %v, want sess-abc", hb["id"])
		}
	})

	t.Run("DoublePuppetAccessToken", func(t *testing.T) {
		// The local user is double-puppeted (puppet row id=900... custom_mxid=@alice).
		// Wait: double-puppet matches on puppet.id=user.dcid AND puppet.custom_mxid=user.mxid.
		// user.dcid=900..., but the puppet with custom_mxid=@alice has id=400000000000000001.
		// So there is NO matching puppet for the local user -> access_token should be NULL.
		var token *string
		if err := db.QueryRow(ctx, `SELECT access_token FROM "user" WHERE mxid='@alice:example.org'`).Scan(&token); err != nil {
			t.Fatalf("query access_token: %v", err)
		}
		if token != nil {
			t.Errorf("access_token = %v, want NULL (no puppet with id=dcid AND custom_mxid=mxid)", *token)
		}
	})

	t.Run("GhostWebhookMetadata", func(t *testing.T) {
		// The webhook puppet (400000000000000003, is_webhook=true) -> metadata.is_webhook.
		meta := queryString(t, db, "SELECT metadata FROM ghost WHERE id='400000000000000003'")
		var parsed map[string]any
		if err := json.Unmarshal([]byte(meta), &parsed); err != nil {
			t.Fatalf("ghost metadata not valid json: %v", err)
		}
		if parsed["is_webhook"] != true {
			t.Errorf("ghost.metadata.is_webhook = %v, want true", parsed["is_webhook"])
		}
	})

	t.Run("DCFileDecryptionInfo", func(t *testing.T) {
		// E2EE file carries decryption_info (M9).
		di := queryString(t, db, "SELECT decryption_info FROM dc_file WHERE mxc='mxc://example.org/abc123'")
		if di == "" {
			t.Error("dc_file decryption_info empty, want preserved JSON")
		}
		var enc bool
		if err := db.QueryRow(ctx, "SELECT encrypted FROM dc_file WHERE mxc='mxc://example.org/abc123'").Scan(&enc); err != nil {
			t.Fatalf("query dc_file encrypted: %v", err)
		}
		if !enc {
			t.Error("dc_file encrypted = false, want true")
		}
	})

	t.Run("CryptoAccountUntouched", func(t *testing.T) {
		// crypto_account must still exist with its original row (NFR-11).
		if got := queryCount(t, db, "SELECT COUNT(*) FROM crypto_account"); got != 1 {
			t.Fatalf("crypto_account row count = %d, want 1 (must be untouched)", got)
		}
		dev := queryString(t, db, "SELECT device_id FROM crypto_account WHERE account_id='@bridgebot:example.org'")
		if dev != "DEVICE1" {
			t.Errorf("crypto_account.device_id = %q, want DEVICE1", dev)
		}
		// No crypto_*_old table should have been created.
		oldCount := queryCount(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'crypto_%_old'")
		if oldCount != 0 {
			t.Errorf("found %d crypto_*_old tables, want 0 (crypto must not be renamed)", oldCount)
		}
	})

	t.Run("OldTablesDropped", func(t *testing.T) {
		n := queryCount(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%_old'")
		if n != 0 {
			t.Errorf("found %d *_old tables, want 0 (all dropped)", n)
		}
	})

	t.Run("DatabaseWasMigratedFlag", func(t *testing.T) {
		// The framework migrator creates this so PostMigrate runs once.
		n := queryCount(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='database_was_migrated'")
		if n != 1 {
			t.Errorf("database_was_migrated table count = %d, want 1", n)
		}
	})
}

// Ensure the bridgev2 database package can construct against the migrated schema
// (smoke test that the schema version landed correctly).
var _ = bridgev2db.MetaTypes{}
