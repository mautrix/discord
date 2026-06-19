// Package discordid contains the encoding/decoding logic between Discord
// snowflakes and bridgev2 networkid types (PortalKey, MessageID, PartID, etc.).
//
// Formats here MUST byte-match the Group 7 legacymigrate.sql transforms.
package discordid

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// DiscordEpoch is the Discord epoch in milliseconds (2015-01-01T00:00:00Z),
// used by SnowflakeToTime: ts_ms = (snowflake >> 22) + DiscordEpoch.
const DiscordEpoch = 1420070400000

// MakePortalID returns the PortalID for a regular channel or DM (= the channel snowflake).
func MakePortalID(channelID string) networkid.PortalID {
	return networkid.PortalID(channelID)
}

// ParsePortalID extracts the channel snowflake from a PortalID.
func ParsePortalID(id networkid.PortalID) (channelID string) {
	return string(id)
}

// MakeGuildPortalID returns the PortalID for a guild-space portal (= the guild snowflake).
func MakeGuildPortalID(guildID string) networkid.PortalID {
	return networkid.PortalID(guildID)
}

// MakeMessageID returns the MessageID for a message (= "channelID-messageID").
// Matches migration SQL: dc_chan_id || '-' || dcid
func MakeMessageID(channelID, messageID string) networkid.MessageID {
	return networkid.MessageID(channelID + "-" + messageID)
}

// ParseMessageID splits a MessageID back into its channel and message snowflakes.
// The format is "channelID-messageID"; returns ok=false if there is no "-".
func ParseMessageID(id networkid.MessageID) (channelID, messageID string, ok bool) {
	s := string(id)
	idx := strings.Index(s, "-")
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// MakeUserID returns the UserID for a Discord user (= the user snowflake).
func MakeUserID(userID string) networkid.UserID {
	return networkid.UserID(userID)
}

// MakeUserLoginID returns the UserLoginID for a Discord user (= the user snowflake).
func MakeUserLoginID(userID string) networkid.UserLoginID {
	return networkid.UserLoginID(userID)
}

// UserIDToUserLoginID converts a UserID to the equivalent UserLoginID.
func UserIDToUserLoginID(userID networkid.UserID) networkid.UserLoginID {
	return networkid.UserLoginID(userID)
}

// MakePartID returns the PartID for an attachment at the given 0-based index.
// Single-part messages (no attachment) use an empty PartID ("").
// Multi-part attachments use "attachment-<index>-<attachmentID>" to byte-match
// the migration SQL window function:
//
//	'attachment-' || (ROW_NUMBER() OVER (... ORDER BY dc_attachment_id)-1) || '-' || dc_attachment_id
func MakePartID(index int, attachmentID string) networkid.PartID {
	if attachmentID == "" {
		return ""
	}
	return networkid.PartID(fmt.Sprintf("attachment-%d-%s", index, attachmentID))
}

// ParsePartID splits a PartID back into its kind ("attachment"), 0-based index,
// and attachment snowflake. Returns kind="" and index=0 for a single-part ("") PartID.
func ParsePartID(id networkid.PartID) (kind string, index int, attachmentID string) {
	s := string(id)
	if s == "" {
		return "", 0, ""
	}
	// Expected format: "attachment-<index>-<attachmentID>"
	const prefix = "attachment-"
	if !strings.HasPrefix(s, prefix) {
		return s, 0, ""
	}
	rest := s[len(prefix):]
	dashIdx := strings.Index(rest, "-")
	if dashIdx < 0 {
		return "attachment", 0, rest
	}
	idx, err := strconv.Atoi(rest[:dashIdx])
	if err != nil {
		return "attachment", 0, rest[dashIdx+1:]
	}
	return "attachment", idx, rest[dashIdx+1:]
}

// MakePortalKey builds the PortalKey for a channel. The Receiver field is only
// set for DMs and group DMs (so each user gets their own portal); guild channels
// have an empty receiver.
func MakePortalKey(channelID string, receiver networkid.UserLoginID, isDM bool) networkid.PortalKey {
	key := networkid.PortalKey{
		ID: MakePortalID(channelID),
	}
	if isDM {
		key.Receiver = receiver
	}
	return key
}

// SnowflakeToTime converts a Discord snowflake into the time it was created.
// Uses: ts_ms = (snowflake >> 22) + DiscordEpoch.
// Returns zero time on parse error.
func SnowflakeToTime(snowflake string) time.Time {
	n, err := strconv.ParseUint(snowflake, 10, 64)
	if err != nil {
		return time.Time{}
	}
	ms := int64(n>>22) + DiscordEpoch
	return time.UnixMilli(ms).UTC()
}
