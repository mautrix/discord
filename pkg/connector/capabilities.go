// Room-scoped capabilities (GetCapabilities on DiscordClient) and general
// capabilities (GetCapabilities on DiscordConnector) logic.
//
// Package-level vars (roomCaps, dmCaps, voiceCaps) are built in init() so
// they are shared across all portal lookups. The method stubs in connector.go
// and client.go call the helpers defined here:
//
//	connector.go: GetCapabilities()     → connectorGeneralCaps
//	connector.go: GetBridgeInfoVersion() → bridgeInfoVersion / bridgeCapabilitiesVersion
//	client.go:    GetCapabilities()     → clientCaps(portal)
//
// Group 1 stubs must be updated to delegate here (see note at bottom of file).
package connector

import (
	"context"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// bridgeInfoVersion is bumped when room metadata (name / topic / avatar)
// semantics change such that all rooms need a bridge-info resend.
const bridgeInfoVersion = 1

// bridgeCapabilitiesVersion is bumped when room features change (e.g. a new
// capability is advertised or withdrawn) so the framework re-sends capability
// state events to all rooms.
const bridgeCapabilitiesVersion = 1

// connectorGeneralCaps is the singleton returned by DiscordConnector.GetCapabilities.
// Discord uses explicit per-channel ACKs (FR-37), so ImplicitReadReceipts is false.
// Discord has no native disappearing-messages feature, so DisappearingMessages is false.
var connectorGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	DisappearingMessages: false,
	AggressiveUpdateInfo: false,
	ImplicitReadReceipts: false,
}

// roomCaps is the capability set for regular guild text/news/forum channels.
// Supports send, edit (own messages), delete (own), reply, reactions
// (including custom emoji), read receipts, typing, and threads.
var roomCaps *event.RoomFeatures

// dmCaps is the capability set for DM and group-DM channels.
// Same as roomCaps but without thread support (Discord DMs have no threads).
var dmCaps *event.RoomFeatures

// voiceCaps is the capability set for voice and stage channels.
// Voice channels only support reactions; sending messages is not supported
// (FR-75 — HandleMatrixMessage must reject sends to voice channels).
var voiceCaps *event.RoomFeatures

func init() {
	roomCaps = &event.RoomFeatures{
		// Discord text messages support send, edit own, delete own.
		Edit:   event.CapLevelFullySupported,
		Delete: event.CapLevelFullySupported,
		// Reactions: fully supported, one per user per emoji (ReactionCount=1),
		// custom guild emoji included (FR-40, FR-71).
		Reaction:             event.CapLevelFullySupported,
		ReactionCount:        1,
		CustomEmojiReactions: true,
		// Replies are supported (FR-30).
		Reply: event.CapLevelFullySupported,
		// Threads are supported in-room (FR-38, FR-39).
		Thread: event.CapLevelFullySupported,
		// Discord tracks read state explicitly (FR-37).
		ReadReceipts:        true,
		TypingNotifications: true,
	}

	dmCaps = &event.RoomFeatures{
		Edit:                 event.CapLevelFullySupported,
		Delete:               event.CapLevelFullySupported,
		Reaction:             event.CapLevelFullySupported,
		ReactionCount:        1,
		CustomEmojiReactions: true,
		Reply:                event.CapLevelFullySupported,
		// DMs have no threads on Discord.
		Thread:              event.CapLevelUnsupported,
		ReadReceipts:        true,
		TypingNotifications: true,
	}

	voiceCaps = &event.RoomFeatures{
		// Voice/stage channels do not accept text messages (FR-75).
		// Edit and delete are also not meaningful without send.
		Edit:   event.CapLevelUnsupported,
		Delete: event.CapLevelUnsupported,
		Reply:  event.CapLevelUnsupported,
		Thread: event.CapLevelUnsupported,
		// Reactions are still supported on pinned messages in voice channels.
		Reaction:             event.CapLevelFullySupported,
		ReactionCount:        1,
		CustomEmojiReactions: true,
		ReadReceipts:         false,
		TypingNotifications:  false,
	}
}

// clientCaps returns the appropriate *event.RoomFeatures for a portal based on
// the stored ChannelType in PortalMeta. Called by DiscordClient.GetCapabilities.
func clientCaps(_ context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		// Fallback for portals without meta (should not happen at runtime).
		return roomCaps
	}
	switch meta.ChannelType {
	case discordgo.ChannelTypeDM, discordgo.ChannelTypeGroupDM:
		return dmCaps
	case discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice:
		return voiceCaps
	default:
		// Guild text, news, forum, threads, and unknown types all get roomCaps.
		return roomCaps
	}
}

// NOTE — duplicate-stub resolution required (Group 2, Task 2.4):
//
// The following two method bodies exist as Group 1 stubs in other files and
// must be updated to delegate to this package's helpers. Until that edit is
// made, the stubs return zero values instead of the real capabilities:
//
//   connector.go  DiscordConnector.GetCapabilities()
//     current:  return &bridgev2.NetworkGeneralCapabilities{}
//     replace:  return connectorGeneralCaps
//
//   connector.go  DiscordConnector.GetBridgeInfoVersion()
//     current:  return 1, 1
//     replace:  return bridgeInfoVersion, bridgeCapabilitiesVersion
//
//   client.go  DiscordClient.GetCapabilities(ctx, portal)
//     current:  return &event.RoomFeatures{}
//     replace:  return clientCaps(ctx, portal)
//
// Go does not allow defining the same method twice; the real implementations
// cannot be declared in this file while the stubs remain in the other files.
