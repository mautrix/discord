// Package upgrades holds the embedded SQL upgrade table for the connector-owned
// Discord tables (dc_guild, dc_role, dc_emoji, dc_file).
package upgrades

import (
	"embed"

	"go.mau.fi/util/dbutil"
)

// Table is the upgrade table for the connector-owned Discord database.
var Table dbutil.UpgradeTable

//go:embed *.sql
var rawUpgrades embed.FS

func init() {
	Table.RegisterFS(rawUpgrades)
}
