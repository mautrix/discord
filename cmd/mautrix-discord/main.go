package main

import (
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"go.mau.fi/mautrix-discord/pkg/connector"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func init() {
	// Wire the connector's legacy config migrator. Without this, the bridgev2
	// framework exits with an error when it detects a legacy (v0.x) config, and
	// the C1/H6 fixes (preserving encryption.pickle_key, direct_media.server_key
	// and avatar_proxy_key) never run. This is the upgrade-path counterpart to
	// the database migration wired in main().
	bridgeconfig.HackyMigrateLegacyNetworkConfig = connector.MigrateLegacyNetworkConfig
}

var c = &connector.DiscordConnector{}
var m = mxmain.BridgeMain{
	Name:        "mautrix-discord",
	URL:         "https://github.com/mautrix/discord",
	Description: "A Matrix-Discord puppeting bridge.",
	Version:     "0.7.0",
	Connector:   c,
}

func main() {
	// Migrate a legacy (mautrix-discord v0.x, schema v24) database to the
	// bridgev2 layout on first start. CheckLegacyDB is a no-op if the database
	// is already bridgev2 (or empty), so this is safe to leave wired permanently.
	//
	// Args: expected legacy DB version (24), the minimum legacy bridge version
	// label and commit that produced a v24-compatible schema, the migration
	// function (rename legacy tables -> *_old, then run the embedded transform
	// SQL up to bridgev2 DB version 29), and allowResume=true so an interrupted
	// migration can be retried.
	//
	// The connector's config migration (MigrateLegacyNetworkConfig, wired via
	// bridgeconfig.HackyMigrateLegacyNetworkConfig in pkg/connector) preserves
	// encryption.pickle_key, direct_media.server_key and avatar_proxy_key; it
	// runs automatically on the config-upgrade path, so no explicit call is
	// needed here.
	m.PostInit = func() {
		m.CheckLegacyDB(
			24,
			"v0.7.6",
			"d7292a0",
			m.LegacyMigrateSimple(legacyMigrateRenameTables, legacyMigrateCopyData, 29),
			true,
		)
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
