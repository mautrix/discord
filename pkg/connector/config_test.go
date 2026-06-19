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

package connector

// Tests for the config-layer migration fixes:
//
//   C1: MigrateLegacyNetworkConfig must write LegacyCryptoPickleKey into
//       encryption.pickle_key when the legacy config has no such key, so the
//       framework upgrader preserves it instead of regenerating a new random key
//       (which would invalidate all existing Olm/Megolm sessions).
//
//   H6: bridge.direct_media.server_key and bridge.avatar_proxy_key must be
//       copied into the new locations so the framework/network upgrader finds
//       them and does NOT regenerate — preserving direct-media URLs and avatar
//       proxy URLs that were already deployed.

import (
	"strings"
	"testing"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
)

const (
	legacyServerKey   = "ed25519:auto AAAA_LEGACY_SERVER_SIGNING_KEY_FOR_TEST"
	legacyAvatarKey   = "legacy-avatar-hmac-key-64-chars-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	legacyUsernameTpl = "discord_{{.}}"
	legacyDisplayTpl  = "{{or .GlobalName .Username}}"
	legacyChanTpl     = "#{{.Name}}"
	legacyGuildTpl    = "{{.Name}}"
)

// legacyCfgYAML is a representative legacy (mautrix-bridge v0.x) config YAML
// with network settings nested under bridge:, no encryption.pickle_key, and
// direct_media / avatar_proxy_key at the bridge.* path.
const legacyCfgYAML = `
appservice:
    database:
        type: sqlite3
        uri: file:mautrix-discord.db?_txlock=immediate

bridge:
    username_template: "` + legacyUsernameTpl + `"
    displayname_template: "` + legacyDisplayTpl + `"
    channel_name_template: "` + legacyChanTpl + `"
    guild_name_template: "` + legacyGuildTpl + `"
    avatar_proxy_key: "` + legacyAvatarKey + `"
    direct_media:
        enabled: true
        server_name: discord-media.example.com
        server_key: "` + legacyServerKey + `"
`

// migrationBaseYAML returns a YAML string that contains every destination path
// that MigrateLegacyNetworkConfig writes to. The Set() helper traverses the base
// map via the *yaml.Node pointer — every destination must pre-exist as a node.
//
// We build this from the embedded ExampleConfig (the network: sub-section) and
// prepend the top-level encryption: and direct_media: stubs that the migration
// also writes to.
func migrationBaseYAML() string {
	// Indent every line of the embedded network example under a "network:" key
	// so the helper can navigate "network" → <key> paths.
	var sb strings.Builder
	sb.WriteString("encryption:\n    pickle_key: generate\ndirect_media:\n    server_key: generate\nnetwork:\n")
	for _, line := range strings.Split(ExampleConfig, "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// makeMigrationHelper builds a CopyHelper with the full migration base and the
// provided legacy cfg YAML.
func makeMigrationHelper(t *testing.T, cfgStr string) *up.CopyHelper {
	t.Helper()
	var baseNode, cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte(migrationBaseYAML()), &baseNode); err != nil {
		t.Fatalf("unmarshal migration base YAML: %v", err)
	}
	if err := yaml.Unmarshal([]byte(cfgStr), &cfgNode); err != nil {
		t.Fatalf("unmarshal cfg YAML: %v", err)
	}
	return up.NewHelper(&baseNode, &cfgNode)
}

// TestMigrateLegacyNetworkConfig_C1_PickleKey verifies that the migration
// writes LegacyCryptoPickleKey into encryption.pickle_key when the legacy
// config has no such key (C1 fix).
//
// Without this fix the framework upgrader finds "generate" and writes a fresh
// random key, silently invalidating all existing Olm/Megolm sessions.
func TestMigrateLegacyNetworkConfig_C1_PickleKey(t *testing.T) {
	helper := makeMigrationHelper(t, legacyCfgYAML)

	// Pre-condition: no encryption.pickle_key in the input cfg.
	if _, ok := helper.Get(up.Str, "encryption", "pickle_key"); ok {
		t.Fatal("precondition: cfg must not have encryption.pickle_key before migration")
	}

	MigrateLegacyNetworkConfig(helper)

	got := helper.GetBase("encryption", "pickle_key")
	if got != LegacyCryptoPickleKey {
		t.Errorf("C1: encryption.pickle_key = %q, want constant %q", got, LegacyCryptoPickleKey)
	}
}

// TestMigrateLegacyNetworkConfig_H6_ServerKey verifies that bridge.direct_media.server_key
// is copied into both:
//   - network.direct_media.server_key (where doUpgradeNetwork reads it)
//   - direct_media.server_key (where the framework bridgeconfig.Upgrader reads it)
//
// Without this fix a fresh signing key is generated and all pre-migration
// direct-media mxc:// URLs return 404 (H6 fix).
func TestMigrateLegacyNetworkConfig_H6_ServerKey(t *testing.T) {
	helper := makeMigrationHelper(t, legacyCfgYAML)
	MigrateLegacyNetworkConfig(helper)

	// network.direct_media.server_key — consumed by doUpgradeNetwork
	gotNetwork := helper.GetBase("network", "direct_media", "server_key")
	if gotNetwork != legacyServerKey {
		t.Errorf("H6: network.direct_media.server_key = %q, want %q", gotNetwork, legacyServerKey)
	}

	// direct_media.server_key — consumed by the framework-level bridgeconfig.Upgrader
	gotTop := helper.GetBase("direct_media", "server_key")
	if gotTop != legacyServerKey {
		t.Errorf("H6: direct_media.server_key = %q, want %q", gotTop, legacyServerKey)
	}
}

// TestMigrateLegacyNetworkConfig_H6_AvatarProxyKey verifies that
// bridge.avatar_proxy_key is copied into network.avatar_proxy_key so that
// doUpgradeNetwork finds a real value and preserves it instead of regenerating
// (H6 fix for avatar proxy URLs).
func TestMigrateLegacyNetworkConfig_H6_AvatarProxyKey(t *testing.T) {
	helper := makeMigrationHelper(t, legacyCfgYAML)
	MigrateLegacyNetworkConfig(helper)

	got := helper.GetBase("network", "avatar_proxy_key")
	if got != legacyAvatarKey {
		t.Errorf("H6: network.avatar_proxy_key = %q, want %q", got, legacyAvatarKey)
	}
}

// TestMigrateLegacyNetworkConfig_NameTemplates verifies that all four name
// templates are copied from the legacy bridge.* path into network.*.
func TestMigrateLegacyNetworkConfig_NameTemplates(t *testing.T) {
	helper := makeMigrationHelper(t, legacyCfgYAML)
	MigrateLegacyNetworkConfig(helper)

	cases := []struct {
		name string
		path []string
		want string
	}{
		{"username_template", []string{"network", "username_template"}, legacyUsernameTpl},
		{"displayname_template", []string{"network", "displayname_template"}, legacyDisplayTpl},
		{"channel_name_template", []string{"network", "channel_name_template"}, legacyChanTpl},
		{"guild_name_template", []string{"network", "guild_name_template"}, legacyGuildTpl},
	}
	for _, tc := range cases {
		got := helper.GetBase(tc.path...)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDoUpgradeNetwork_PreservesServerKey exercises doUpgradeNetwork (the
// exported networkUpgrader) to confirm that a pre-populated server_key value
// is copied rather than regenerated. This mirrors what happens after
// MigrateLegacyNetworkConfig seeds network.direct_media.server_key into the cfg.
func TestDoUpgradeNetwork_PreservesServerKey(t *testing.T) {
	// The cfg node has a real server_key (as placed by MigrateLegacyNetworkConfig).
	// The base is the full ExampleConfig, indented under "network:".
	cfgYAML := `
network:
    username_template: "discord_{{.}}"
    displayname_template: "{{or .GlobalName .Username}}"
    channel_name_template: "#{{.Name}}"
    guild_name_template: "{{.Name}}"
    avatar_proxy_key: "` + legacyAvatarKey + `"
    backfill:
        initial:
            dm: 0
            channel: 0
            thread: 0
        missed:
            dm: 0
            channel: 0
            thread: 0
        max_guild_members: -1
    animated_sticker:
        target: webp
        args:
            width: 320
            height: 320
            fps: 25
    direct_media:
        enabled: false
        server_name: discord-media.example.com
        well_known_response: ""
        allow_proxy: true
        server_key: "` + legacyServerKey + `"
    restricted_rooms: false
    custom_emoji_reactions: true
    enable_webhook_avatars: false
    forbid_dming_strangers: true
    delete_portal_on_channel_delete: false
    delete_guild_on_leave: true
    sync_direct_chat_list: false
    cache_media: unencrypted
    private_chat_portal_meta: false
    federate_rooms: true
    portal_message_buffer: 128
    use_discord_cdn_upload: true
    prefix_webhook_messages: true
    proxy: ""
    public_address: ""
`

	// Base: ExampleConfig indented under "network:" so the upgrader can navigate
	// the network.* paths to copy values into.
	var sb strings.Builder
	sb.WriteString("network:\n")
	for _, line := range strings.Split(ExampleConfig, "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	var baseNode, cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte(sb.String()), &baseNode); err != nil {
		t.Fatalf("unmarshal base: %v", err)
	}
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfgNode); err != nil {
		t.Fatalf("unmarshal cfg: %v", err)
	}

	helper := up.NewHelper(&baseNode, &cfgNode)
	networkUpgrader.DoUpgrade(helper)

	got := helper.GetBase("network", "direct_media", "server_key")
	if got == "" || got == "generate" {
		t.Errorf("doUpgradeNetwork: server_key was regenerated or empty, got %q", got)
	}
	if got != legacyServerKey {
		t.Errorf("doUpgradeNetwork: network.direct_media.server_key = %q, want %q", got, legacyServerKey)
	}
}

// TestDoUpgradeNetwork_PreservesAvatarProxyKey exercises doUpgradeNetwork to
// confirm that a pre-populated avatar_proxy_key is copied rather than regenerated.
func TestDoUpgradeNetwork_PreservesAvatarProxyKey(t *testing.T) {
	cfgYAML := `
network:
    username_template: "discord_{{.}}"
    displayname_template: "{{or .GlobalName .Username}}"
    channel_name_template: "#{{.Name}}"
    guild_name_template: "{{.Name}}"
    avatar_proxy_key: "` + legacyAvatarKey + `"
    backfill:
        initial:
            dm: 0
            channel: 0
            thread: 0
        missed:
            dm: 0
            channel: 0
            thread: 0
        max_guild_members: -1
    animated_sticker:
        target: webp
        args:
            width: 320
            height: 320
            fps: 25
    direct_media:
        enabled: false
        server_name: discord-media.example.com
        well_known_response: ""
        allow_proxy: true
        server_key: "` + legacyServerKey + `"
    restricted_rooms: false
    custom_emoji_reactions: true
    enable_webhook_avatars: false
    forbid_dming_strangers: true
    delete_portal_on_channel_delete: false
    delete_guild_on_leave: true
    sync_direct_chat_list: false
    cache_media: unencrypted
    private_chat_portal_meta: false
    federate_rooms: true
    portal_message_buffer: 128
    use_discord_cdn_upload: true
    prefix_webhook_messages: true
    proxy: ""
    public_address: ""
`

	var sb strings.Builder
	sb.WriteString("network:\n")
	for _, line := range strings.Split(ExampleConfig, "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	var baseNode, cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte(sb.String()), &baseNode); err != nil {
		t.Fatalf("unmarshal base: %v", err)
	}
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfgNode); err != nil {
		t.Fatalf("unmarshal cfg: %v", err)
	}

	helper := up.NewHelper(&baseNode, &cfgNode)
	networkUpgrader.DoUpgrade(helper)

	got := helper.GetBase("network", "avatar_proxy_key")
	if got == "" || got == "generate" {
		t.Errorf("doUpgradeNetwork: avatar_proxy_key was regenerated or empty, got %q", got)
	}
	if got != legacyAvatarKey {
		t.Errorf("doUpgradeNetwork: network.avatar_proxy_key = %q, want %q", got, legacyAvatarKey)
	}
}

// TestC1_NoPriorPickleKey_SetsLegacyConstant is an integration-style assertion
// that after migration, the base holds the constant — even with no encryption:
// section at all in the cfg input.
func TestC1_NoPriorPickleKey_SetsLegacyConstant(t *testing.T) {
	cfgNoEncryption := `
appservice:
    database:
        type: sqlite3
        uri: file:mautrix-discord.db?_txlock=immediate
bridge:
    avatar_proxy_key: "` + legacyAvatarKey + `"
    direct_media:
        server_key: "` + legacyServerKey + `"
`
	helper := makeMigrationHelper(t, cfgNoEncryption)

	// Confirm no pickle_key in cfg before migration.
	if _, ok := helper.Get(up.Str, "encryption", "pickle_key"); ok {
		t.Fatal("precondition: cfg should not have encryption.pickle_key before migration")
	}

	MigrateLegacyNetworkConfig(helper)

	// After migration the base must contain the legacy constant.
	got := helper.GetBase("encryption", "pickle_key")
	if got != LegacyCryptoPickleKey {
		t.Errorf("C1: encryption.pickle_key = %q, want constant %q", got, LegacyCryptoPickleKey)
	}
}

// TestCopyToOtherLocation_Idempotent verifies that calling CopyToOtherLocation
// twice with the same source/dest is idempotent — the second call does not
// corrupt the value written by the first.
func TestCopyToOtherLocation_Idempotent(t *testing.T) {
	helper := makeMigrationHelper(t, legacyCfgYAML)

	bridgeconfig.CopyToOtherLocation(helper, up.Str,
		[]string{"bridge", "direct_media", "server_key"},
		[]string{"network", "direct_media", "server_key"},
	)
	first := helper.GetBase("network", "direct_media", "server_key")

	bridgeconfig.CopyToOtherLocation(helper, up.Str,
		[]string{"bridge", "direct_media", "server_key"},
		[]string{"network", "direct_media", "server_key"},
	)
	second := helper.GetBase("network", "direct_media", "server_key")

	if first != legacyServerKey {
		t.Errorf("first copy: got %q, want %q", first, legacyServerKey)
	}
	if second != legacyServerKey {
		t.Errorf("second copy (idempotent): got %q, want %q", second, legacyServerKey)
	}
}
