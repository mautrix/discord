// Package connector implements the Discord network connector for the bridgev2
// framework. This file holds the DiscordConnector struct (one per bridge
// process) and its NetworkConnector implementation.
//
// Group 1 scaffolding: every method is a compiling stub. The compile-time
// interface assertions at the bottom of this file are the forcing function that
// keeps the signatures correct as later groups fill them in.
package connector

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
)

// snowflakeRe matches a Discord snowflake: 17–20 decimal digits.
var snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

// DiscordConnector is the top-level network connector for the Discord bridge.
type DiscordConnector struct {
	br     *bridgev2.Bridge
	Config DiscordConfig

	// DB is the connector-owned database (dc_role/dc_file/dc_emoji/dc_guild).
	// Populated in Init; tables upgraded in Start.
	DB *discorddb.DiscordDB

	// maxFileSize is set by the framework when it learns the homeserver's max
	// upload size. Protected by maxFileSizeMu.
	maxFileSize   int64
	maxFileSizeMu sync.RWMutex

	// roleCache is an in-memory cache for guild roles.
	// Key: guildID + "-" + roleID.  Populated by GUILD_CREATE/GUILD_ROLE_* handlers.
	// TODO(group4): populated in handlediscord.go
	roleCache   map[string]string // roleID → role name (placeholder; real cache in group4)
	roleCacheMu sync.RWMutex
}

// Init stores the bridge reference and initialises connector-level caches.
// Must not do any network/blocking work (FR-5 — LoadUserLogin is no-I/O).
func (dc *DiscordConnector) Init(br *bridgev2.Bridge) {
	dc.br = br

	// Initialise the connector-owned database wrapper. Upgrade is deferred to
	// Start so it runs inside the bridge's startup context.
	dc.DB = discorddb.New(br.DB.Database)

	// Initialise in-memory caches.
	dc.roleCache = make(map[string]string)
}

// Start performs non-user-specific startup: upgrades the connector DB tables
// and registers media routes.
func (dc *DiscordConnector) Start(ctx context.Context) error {
	if err := dc.DB.Upgrade(ctx); err != nil {
		return fmt.Errorf("discorddb upgrade: %w", err)
	}
	// FR-60: register the avatar-proxy HTTP route when public_address is set.
	// The framework registers direct-media routes itself once SetUseDirectMedia
	// is called; the avatar proxy is a separate connector-owned endpoint.
	if srv, ok := dc.br.Matrix.(bridgev2.MatrixConnectorWithServer); ok {
		if router := srv.GetRouter(); router != nil {
			dc.registerAvatarProxyRoute(router)
		}
	}
	return nil
}

// Stop is called on bridge shutdown after all clients have disconnected.
func (dc *DiscordConnector) Stop() {
	// Nothing to tear down at the connector level — individual clients
	// disconnect themselves in DiscordClient.Disconnect.
}

// GetName returns the bridge metadata used to fill m.bridge events and the
// example config. Must work without a loaded config.
func (dc *DiscordConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "Discord",
		NetworkURL:           "https://discord.com",
		NetworkID:            "discord",
		BeeperBridgeType:     "github.com/mautrix/discord",
		DefaultPort:          29334,
		DefaultCommandPrefix: "!discord",
	}
}

// GetCapabilities returns the general (non-room-scoped) capabilities.
func (dc *DiscordConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	// TODO(group2)
	return &bridgev2.NetworkGeneralCapabilities{}
}

// LoadUserLogin builds a *DiscordClient for a loaded UserLogin.
// This must not perform any network I/O — Discord gateway connect happens
// inside DiscordClient.Connect (FR-5).
func (dc *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*UserLoginMeta)
	if !ok || meta == nil {
		// Metadata not yet set (e.g. fresh login before first save). Create an
		// empty placeholder so the client can be initialised safely.
		meta = &UserLoginMeta{}
	}
	login.Client = &DiscordClient{
		br:        dc.br,
		userLogin: login,
		connector: dc,
		meta:      meta,
	}
	return nil
}

// GetLoginFlows returns the supported login flows.
// FR-1: token flow; FR-2: QR remote-auth flow.
func (dc *DiscordConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			ID:          "token",
			Name:        "Token",
			Description: "Log in with a Discord user or bot token.",
		},
		{
			ID:          "qr",
			Name:        "QR Code",
			Description: "Log in by scanning a QR code with the Discord mobile app.",
		},
	}
}

// CreateLogin dispatches to the correct login process for the given flow ID.
// FR-1 → TokenLoginProcess; FR-2 → QRLoginProcess.
func (dc *DiscordConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case "token":
		return &TokenLoginProcess{
			Main: dc,
			User: user,
		}, nil
	case "qr":
		return &QRLoginProcess{
			Main: dc,
			User: user,
		}, nil
	default:
		return nil, fmt.Errorf("unknown login flow %q", flowID)
	}
}

// GetBridgeInfoVersion returns the bridge-info and capabilities version numbers.
func (dc *DiscordConnector) GetBridgeInfoVersion() (info, capabilities int) {
	// TODO(group2)
	return 1, 1
}

// --- IdentifierValidatingNetwork ---

// ValidateUserID checks that the ID looks like a Discord snowflake (17–20 digits).
func (dc *DiscordConnector) ValidateUserID(id networkid.UserID) bool {
	return snowflakeRe.MatchString(string(id))
}

// --- ConfigValidatingNetwork ---

// ValidateConfig validates token format and template syntax.
func (dc *DiscordConnector) ValidateConfig() error {
	// TODO(group2)
	return nil
}

// --- MaxFileSizeingNetwork ---

// SetMaxFileSize records the homeserver's max upload size (in bytes).
func (dc *DiscordConnector) SetMaxFileSize(maxSize int64) {
	dc.maxFileSizeMu.Lock()
	dc.maxFileSize = maxSize
	dc.maxFileSizeMu.Unlock()
}

// resolveCustomEmojiMXC is implemented in convertdiscord.go (Task 4.2): it
// downloads the emoji from the Discord CDN and uploads it to Matrix via the bot
// intent, caching the result in dc_file.

// Compile-time interface assertions — the forcing function for Group 1.
var (
	_ bridgev2.NetworkConnector            = (*DiscordConnector)(nil)
	_ bridgev2.StoppableNetwork            = (*DiscordConnector)(nil)
	_ bridgev2.DirectMediableNetwork       = (*DiscordConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork       = (*DiscordConnector)(nil)
	_ bridgev2.ConfigValidatingNetwork     = (*DiscordConnector)(nil)
	_ bridgev2.IdentifierValidatingNetwork = (*DiscordConnector)(nil)
)
