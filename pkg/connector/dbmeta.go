// PortalMeta, GhostMeta, MessageMeta, UserLoginMeta, ReactionMeta and GetDBMetaTypes
// Implemented in Group 2 (Task 2.3).
package connector

import (
	"time"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2/database"
)

// BridgingMode controls which Discord channels get a Matrix portal created for them.
type BridgingMode string

const (
	// BridgingModeNothing bridges no channels at all.
	BridgingModeNothing BridgingMode = "nothing"
	// BridgingModeIfPortalExists only bridges channels that already have a portal.
	BridgingModeIfPortalExists BridgingMode = "if-portal-exists"
	// BridgingModeCreateOnMessage creates a portal when a message is received.
	BridgingModeCreateOnMessage BridgingMode = "create-on-message"
	// BridgingModeEverything pre-creates portals for all channels.
	BridgingModeEverything BridgingMode = "everything"
)

// TokenType distinguishes the kind of Discord credential used to log in.
type TokenType string

const (
	// TokenTypeUser is a plain user account token.
	TokenTypeUser TokenType = "user"
	// TokenTypeBot is a bot application token.
	TokenTypeBot TokenType = "bot"
	// TokenTypeOAuth is an OAuth2 bearer token.
	TokenTypeOAuth TokenType = "oauth"
)

// PortalMeta is stored in the portal.metadata JSON column.
type PortalMeta struct {
	// ChannelType is the Discord channel type (text/voice/news/forum/dm/group_dm).
	ChannelType discordgo.ChannelType `json:"channel_type"`
	// GuildID is the guild snowflake; empty for DMs.
	GuildID string `json:"guild_id,omitempty"`
	// GuildBridgingMode controls portal-creation gating for guild channels — FR-45, OQ-10.
	GuildBridgingMode BridgingMode `json:"guild_bridging_mode,omitempty"`
	// NSFW is the NSFW flag used in channel name templates — FR-74.
	NSFW bool `json:"nsfw,omitempty"`
	// RelayWebhookID and RelayWebhookSecret hold legacy relay webhook credentials — FR-44.
	RelayWebhookID     string `json:"relay_webhook_id,omitempty"`
	RelayWebhookSecret string `json:"relay_webhook_secret,omitempty"`
}

// GhostMeta is stored in the ghost.metadata JSON column.
type GhostMeta struct {
	// IsWebhook indicates the ghost represents a webhook/bot sender — FR-31, FR-49.
	IsWebhook bool `json:"is_webhook,omitempty"`
}

// MessageMeta is stored in the message.metadata JSON column.
type MessageMeta struct {
	// DiscordID is the Discord message snowflake (cached for edit/delete) — FR-29.
	DiscordID string `json:"discord_id,omitempty"`
	// EditTimestamp records the last Discord edit time for ordering — FR-29.
	EditTimestamp *time.Time `json:"edit_ts,omitempty"`
}

// UserLoginMeta is stored in the user_login.metadata JSON column.
type UserLoginMeta struct {
	// Token is the Discord credential — FR-1, FR-5.
	Token string `json:"token"`
	// TokenType distinguishes user/bot/oauth tokens — FR-1.
	TokenType TokenType `json:"token_type"`
	// GatewaySessionID and GatewaySequenceNum persist the gateway session for RESUME — FR-6.
	GatewaySessionID   string `json:"gateway_session_id,omitempty"`
	GatewaySequenceNum int    `json:"gateway_sequence_num,omitempty"`
	// ReadStateVersion is sent with ChannelMessageAck to prevent read-state races — FR-37, OQ-13.
	ReadStateVersion int `json:"read_state_version,omitempty"`
	// RelationshipsReady indicates the relationship cache has been fully populated — FR-66.
	RelationshipsReady bool `json:"relationships_ready,omitempty"`
}

// ReactionMeta is stored in the reaction.metadata JSON column.
// Discord reactions carry no connector-specific state beyond what the framework stores.
type ReactionMeta struct{}

// GetDBMetaTypes returns the typed metadata struct constructors for every
// framework-managed table. Called once at bridge startup.
func (dc *DiscordConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal:    func() any { return &PortalMeta{} },
		Ghost:     func() any { return &GhostMeta{} },
		Message:   func() any { return &MessageMeta{} },
		Reaction:  func() any { return &ReactionMeta{} },
		UserLogin: func() any { return &UserLoginMeta{} },
	}
}
