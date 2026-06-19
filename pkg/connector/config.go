// DiscordConfig struct and example-config.yaml embed
// Implemented in Group 2 (Task 2.2).
package connector

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	up "go.mau.fi/util/configupgrade"
	"go.mau.fi/util/random"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/federation"
)

// LegacyCryptoPickleKey is the hardcoded pickle key used by every legacy
// Go mautrix bridge (discord, whatsapp, etc.). It must be written verbatim
// into encryption.pickle_key when migrating an old config; otherwise the
// bridgev2 framework generates a new random key and all existing Olm/Megolm
// sessions become undecryptable (C1).
//
// The constant originates from legacy main.go:
//
//	CryptoPickleKey: "maunium.net/go/mautrix-whatsapp"
//
// Do not change this value.
const LegacyCryptoPickleKey = "maunium.net/go/mautrix-whatsapp"

//go:embed example-config.yaml
var ExampleConfig string

// DiscordConfig holds all Discord-connector-specific configuration, parsed
// from the network: section of the bridge config file.
type DiscordConfig struct {
	// Name templates
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`
	ChannelNameTemplate string `yaml:"channel_name_template"`
	GuildNameTemplate   string `yaml:"guild_name_template"`

	// Behaviour toggles
	RestrictedRooms             bool   `yaml:"restricted_rooms"`
	CustomEmojiReactions        bool   `yaml:"custom_emoji_reactions"`
	EnableWebhookAvatars        bool   `yaml:"enable_webhook_avatars"`
	ForbidDMingStrangers        bool   `yaml:"forbid_dming_strangers"`
	DeletePortalOnChannelDelete bool   `yaml:"delete_portal_on_channel_delete"`
	DeleteGuildOnLeave          bool   `yaml:"delete_guild_on_leave"`
	SyncDirectChatList          bool   `yaml:"sync_direct_chat_list"`
	CacheMedia                  string `yaml:"cache_media"`
	PrivateChatPortalMeta       bool   `yaml:"private_chat_portal_meta"`
	FederateRooms               bool   `yaml:"federate_rooms"`
	PortalMessageBuffer         int    `yaml:"portal_message_buffer"`
	UseDiscordCDNUpload         bool   `yaml:"use_discord_cdn_upload"`
	PrefixWebhookMessages       bool   `yaml:"prefix_webhook_messages"`
	Proxy                       string `yaml:"proxy"`
	PublicAddress               string `yaml:"public_address"`
	AvatarProxyKey              string `yaml:"avatar_proxy_key"`

	// Backfill limits
	Backfill struct {
		Initial struct {
			DM      int `yaml:"dm"`
			Channel int `yaml:"channel"`
			Thread  int `yaml:"thread"`
		} `yaml:"initial"`
		Missed struct {
			DM      int `yaml:"dm"`
			Channel int `yaml:"channel"`
			Thread  int `yaml:"thread"`
		} `yaml:"missed"`
		MaxGuildMembers int `yaml:"max_guild_members"`
	} `yaml:"backfill"`

	// Animated sticker conversion
	AnimatedSticker struct {
		Target string `yaml:"target"`
		Args   struct {
			Width  int `yaml:"width"`
			Height int `yaml:"height"`
			FPS    int `yaml:"fps"`
		} `yaml:"args"`
	} `yaml:"animated_sticker"`

	// Direct media (custom mxc:// URIs instead of re-uploading)
	DirectMedia struct {
		Enabled           bool   `yaml:"enabled"`
		ServerName        string `yaml:"server_name"`
		WellKnownResponse string `yaml:"well_known_response"`
		AllowProxy        bool   `yaml:"allow_proxy"`
		ServerKey         string `yaml:"server_key"`
	} `yaml:"direct_media"`

	// Compiled templates — populated by parseTemplates, not from YAML.
	displaynameTpl *template.Template
	channelNameTpl *template.Template
	guildNameTpl   *template.Template
}

// parseTemplates compiles all name templates. Returns the first compilation error.
func (c *DiscordConfig) parseTemplates() error {
	var err error
	if c.displaynameTpl, err = template.New("displayname").Parse(c.DisplaynameTemplate); err != nil {
		return fmt.Errorf("invalid displayname_template: %w", err)
	}
	if c.channelNameTpl, err = template.New("channel_name").Parse(c.ChannelNameTemplate); err != nil {
		return fmt.Errorf("invalid channel_name_template: %w", err)
	}
	if c.guildNameTpl, err = template.New("guild_name").Parse(c.GuildNameTemplate); err != nil {
		return fmt.Errorf("invalid guild_name_template: %w", err)
	}
	return nil
}

// FormatDisplayname executes the displayname_template against the provided data.
func (c *DiscordConfig) FormatDisplayname(data any) (string, error) {
	return execTemplate(c.displaynameTpl, data)
}

// FormatChannelName executes the channel_name_template against the provided data.
func (c *DiscordConfig) FormatChannelName(data any) (string, error) {
	return execTemplate(c.channelNameTpl, data)
}

// FormatGuildName executes the guild_name_template against the provided data.
func (c *DiscordConfig) FormatGuildName(data any) (string, error) {
	return execTemplate(c.guildNameTpl, data)
}

func execTemplate(tpl *template.Template, data any) (string, error) {
	if tpl == nil {
		return "", fmt.Errorf("template not compiled")
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// doUpgradeNetwork copies all network-section keys. Called by the upgrader
// returned from GetConfig.
func doUpgradeNetwork(helper up.Helper) {
	helper.Copy(up.Str, "network", "username_template")
	helper.Copy(up.Str, "network", "displayname_template")
	helper.Copy(up.Str, "network", "channel_name_template")
	helper.Copy(up.Str, "network", "guild_name_template")

	helper.Copy(up.Bool, "network", "restricted_rooms")
	helper.Copy(up.Bool, "network", "custom_emoji_reactions")
	helper.Copy(up.Bool, "network", "enable_webhook_avatars")
	helper.Copy(up.Bool, "network", "forbid_dming_strangers")
	helper.Copy(up.Bool, "network", "delete_portal_on_channel_delete")
	helper.Copy(up.Bool, "network", "delete_guild_on_leave")
	helper.Copy(up.Bool, "network", "sync_direct_chat_list")
	helper.Copy(up.Str, "network", "cache_media")
	helper.Copy(up.Bool, "network", "private_chat_portal_meta")
	helper.Copy(up.Bool, "network", "federate_rooms")
	helper.Copy(up.Int, "network", "portal_message_buffer")
	helper.Copy(up.Bool, "network", "use_discord_cdn_upload")
	helper.Copy(up.Bool, "network", "prefix_webhook_messages")
	helper.Copy(up.Str|up.Null, "network", "proxy")
	helper.Copy(up.Str|up.Null, "network", "public_address")

	// avatar_proxy_key: generate a random key on first run, preserve thereafter.
	if key, ok := helper.Get(up.Str, "network", "avatar_proxy_key"); !ok || key == "generate" {
		helper.Set(up.Str, random.String(64), "network", "avatar_proxy_key")
	} else {
		helper.Copy(up.Str, "network", "avatar_proxy_key")
	}

	helper.Copy(up.Int, "network", "backfill", "initial", "dm")
	helper.Copy(up.Int, "network", "backfill", "initial", "channel")
	helper.Copy(up.Int, "network", "backfill", "initial", "thread")
	helper.Copy(up.Int, "network", "backfill", "missed", "dm")
	helper.Copy(up.Int, "network", "backfill", "missed", "channel")
	helper.Copy(up.Int, "network", "backfill", "missed", "thread")
	helper.Copy(up.Int, "network", "backfill", "max_guild_members")

	helper.Copy(up.Str, "network", "animated_sticker", "target")
	helper.Copy(up.Int, "network", "animated_sticker", "args", "width")
	helper.Copy(up.Int, "network", "animated_sticker", "args", "height")
	helper.Copy(up.Int, "network", "animated_sticker", "args", "fps")

	helper.Copy(up.Bool, "network", "direct_media", "enabled")
	helper.Copy(up.Str, "network", "direct_media", "server_name")
	helper.Copy(up.Str|up.Null, "network", "direct_media", "well_known_response")
	helper.Copy(up.Bool, "network", "direct_media", "allow_proxy")
	// server_key: generate a signing key on first run, preserve thereafter.
	if key, ok := helper.Get(up.Str, "network", "direct_media", "server_key"); !ok || key == "generate" {
		helper.Set(up.Str, federation.GenerateSigningKey().SynapseString(), "network", "direct_media", "server_key")
	} else {
		helper.Copy(up.Str, "network", "direct_media", "server_key")
	}
}

// MigrateLegacyNetworkConfig migrates network-specific keys from a legacy
// (mautrix-bridge v0.x) config into the bridgev2 layout. It must be assigned
// to bridgeconfig.HackyMigrateLegacyNetworkConfig in main.go's init():
//
//	func init() {
//	    bridgeconfig.HackyMigrateLegacyNetworkConfig = connector.MigrateLegacyNetworkConfig
//	}
//
// Responsibilities:
//
//  1. C1 — pickle_key: the legacy binary hardcoded CryptoPickleKey as a Go
//     constant and never wrote it into the YAML. The bridgev2 framework treats
//     a missing or "generate" pickle_key as a signal to generate a NEW random
//     key, which silently invalidates all existing Olm/Megolm sessions. We
//     write the legacy constant verbatim into encryption.pickle_key so the
//     framework preserves it via its normal Copy path.
//
//  2. H6 — direct_media.server_key: the framework upgrader copies keys under
//     direct_media.* only from the new layout. Legacy configs stored this value
//     at bridge.direct_media.server_key. Without this copy a new random signing
//     key is generated and all pre-migration direct-media URLs return 404.
//
//  3. H6 — avatar_proxy_key: stored at bridge.avatar_proxy_key in the legacy
//     config. Without migration a new random key is generated and all
//     pre-migration avatar proxy URLs break.
//
//  4. All legacy network toggles are mapped from bridge.* → network.*.
func MigrateLegacyNetworkConfig(helper up.Helper) {
	// C1: Write the hardcoded legacy pickle_key so the framework copies it
	// rather than regenerating. Only set when not already in the new location.
	if _, ok := helper.Get(up.Str, "encryption", "pickle_key"); !ok {
		helper.Set(up.Str, LegacyCryptoPickleKey, "encryption", "pickle_key")
	}

	// H6: direct_media.server_key — copy legacy path so the framework upgrader
	// (which reads direct_media.server_key at the top level) preserves it.
	bridgeconfig.CopyToOtherLocation(
		helper, up.Str,
		[]string{"bridge", "direct_media", "server_key"},
		[]string{"direct_media", "server_key"},
	)
	// Also write into network.direct_media.server_key so doUpgradeNetwork finds it.
	bridgeconfig.CopyToOtherLocation(
		helper, up.Str,
		[]string{"bridge", "direct_media", "server_key"},
		[]string{"network", "direct_media", "server_key"},
	)

	// H6: avatar_proxy_key — copy legacy path so doUpgradeNetwork finds it and
	// does not generate a new key.
	bridgeconfig.CopyToOtherLocation(
		helper, up.Str,
		[]string{"bridge", "avatar_proxy_key"},
		[]string{"network", "avatar_proxy_key"},
	)

	// Name templates
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "username_template"}, []string{"network", "username_template"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "displayname_template"}, []string{"network", "displayname_template"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "channel_name_template"}, []string{"network", "channel_name_template"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "guild_name_template"}, []string{"network", "guild_name_template"})

	// Behaviour toggles
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "restricted_rooms"}, []string{"network", "restricted_rooms"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "custom_emoji_reactions"}, []string{"network", "custom_emoji_reactions"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "enable_webhook_avatars"}, []string{"network", "enable_webhook_avatars"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "forbid_dming_strangers"}, []string{"network", "forbid_dming_strangers"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "delete_portal_on_channel_delete"}, []string{"network", "delete_portal_on_channel_delete"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "delete_guild_on_leave"}, []string{"network", "delete_guild_on_leave"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "sync_direct_chat_list"}, []string{"network", "sync_direct_chat_list"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "cache_media"}, []string{"network", "cache_media"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "federate_rooms"}, []string{"network", "federate_rooms"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "portal_message_buffer"}, []string{"network", "portal_message_buffer"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "use_discord_cdn_upload"}, []string{"network", "use_discord_cdn_upload"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "prefix_webhook_messages"}, []string{"network", "prefix_webhook_messages"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str|up.Null, []string{"bridge", "proxy"}, []string{"network", "proxy"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str|up.Null, []string{"bridge", "public_address"}, []string{"network", "public_address"})

	// Backfill limits (bridge.backfill.forward_limits.* → network.backfill.*)
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "initial", "dm"}, []string{"network", "backfill", "initial", "dm"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "initial", "channel"}, []string{"network", "backfill", "initial", "channel"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "initial", "thread"}, []string{"network", "backfill", "initial", "thread"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "missed", "dm"}, []string{"network", "backfill", "missed", "dm"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "missed", "channel"}, []string{"network", "backfill", "missed", "channel"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "forward_limits", "missed", "thread"}, []string{"network", "backfill", "missed", "thread"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "backfill", "max_guild_members"}, []string{"network", "backfill", "max_guild_members"})

	// Animated sticker settings (bridge.animated_sticker.* → network.animated_sticker.*)
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "animated_sticker", "target"}, []string{"network", "animated_sticker", "target"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "animated_sticker", "args", "width"}, []string{"network", "animated_sticker", "args", "width"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "animated_sticker", "args", "height"}, []string{"network", "animated_sticker", "args", "height"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "animated_sticker", "args", "fps"}, []string{"network", "animated_sticker", "args", "fps"})

	// Direct media settings (bridge.direct_media.* → network.direct_media.*)
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "direct_media", "enabled"}, []string{"network", "direct_media", "enabled"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "direct_media", "server_name"}, []string{"network", "direct_media", "server_name"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str|up.Null, []string{"bridge", "direct_media", "well_known_response"}, []string{"network", "direct_media", "well_known_response"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "direct_media", "allow_proxy"}, []string{"network", "direct_media", "allow_proxy"})
}

// networkUpgrader is the configupgrade.Upgrader for the network: section only.
// Returned by GetConfig and wraps doUpgradeNetwork.
var networkUpgrader up.SpacedUpgrader = &up.StructUpgrader{
	SimpleUpgrader: up.SimpleUpgrader(doUpgradeNetwork),
	Blocks: [][]string{
		{"network"},
		{"network", "backfill"},
		{"network", "animated_sticker"},
		{"network", "direct_media"},
	},
}

// GetConfig implements bridgev2.NetworkConnector.
// Returns the embedded example config, a *DiscordConfig for unmarshalling, and
// the network-section upgrader.
func (dc *DiscordConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &dc.Config, networkUpgrader
}
