// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

import (
	"context"

	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var DiscordGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	// Aggressive ghost info updates let us refresh ghost profiles during
	// backfill and when handling incoming messages, edits, and reactions.
	// Otherwise, mautrix skips updating ghosts that already have a name and
	// avatar set[1].
	//
	// Also, generally, it is cheap for DiscordClient's GetUserInfo to be
	// called as it should mostly hit the user cache. Otherwise, it asks
	// Discord's REST API for the user, which the first-party client itself
	// does fairly liberally. Regardless, no Matrix calls occur if the profile
	// didn't actually change, as Mautrix diffs.
	//
	// [1]: https://github.com/mautrix/go/blob/2b8e6caf43bea108e7fccacfa793fe46069ce3a8/bridgev2/ghost.go#L295
	AggressiveUpdateInfo: true,

	Provisioning: bridgev2.ProvisioningCapabilities{
		ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{},
		GroupCreation:     map[string]bridgev2.GroupTypeCapabilities{},
	},
}

func (d *DiscordConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return DiscordGeneralCaps
}

func (d *DiscordConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 4
}

/*func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}*/

func capID() string {
	base := "fi.mau.discord.capabilities.2026_03_18"
	if ffmpeg.Supported() {
		return base + "+ffmpeg"
	}
	return base
}

// TODO: This limit is increased depending on user subscription status (Discord Nitro).
const MaxTextLength = 2000

// TODO: This limit is increased depending on user subscription status (Discord Nitro).
// TODO: Verify this figure (10 MiB).
const MaxFileSize = 10485760

var discordCaps = &event.RoomFeatures{
	ID:       capID(),
	Reply:    event.CapLevelFullySupported,
	Reaction: event.CapLevelFullySupported,
	Edit:     event.CapLevelFullySupported,
	Delete:   event.CapLevelFullySupported,
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:               event.CapLevelFullySupported,
		event.FmtItalic:             event.CapLevelFullySupported,
		event.FmtStrikethrough:      event.CapLevelFullySupported,
		event.FmtInlineCode:         event.CapLevelFullySupported,
		event.FmtCodeBlock:          event.CapLevelFullySupported,
		event.FmtSyntaxHighlighting: event.CapLevelFullySupported,
		event.FmtBlockquote:         event.CapLevelFullySupported,
		event.FmtInlineLink:         event.CapLevelFullySupported,
		event.FmtUserLink:           event.CapLevelFullySupported,
		event.FmtRoomLink:           event.CapLevelUnsupported, // TODO: Support.
		event.FmtEventLink:          event.CapLevelUnsupported, // TODO: Support.
		event.FmtAtRoomMention:      event.CapLevelUnsupported, // TODO: Support.
		event.FmtUnorderedList:      event.CapLevelFullySupported,
		event.FmtOrderedList:        event.CapLevelFullySupported,
		event.FmtListStart:          event.CapLevelFullySupported,
		event.FmtListJumpValue:      event.CapLevelUnsupported,
		event.FmtCustomEmoji:        event.CapLevelUnsupported, // TODO: Support.
	},
	File: event.FileFeatureMap{
		event.MsgImage: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/jpeg": event.CapLevelFullySupported,
				"image/png":  event.CapLevelFullySupported,
				"image/gif":  event.CapLevelFullySupported,
				"image/webp": event.CapLevelFullySupported,
				"image/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4":  event.CapLevelFullySupported,
				"video/webm": event.CapLevelFullySupported,
				"video/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgAudio: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/mpeg": event.CapLevelFullySupported,
				"audio/webm": event.CapLevelFullySupported,
				"audio/wav":  event.CapLevelFullySupported,
				"audio/*":    event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgVoice: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/ogg; codecs=opus":  event.CapLevelFullySupported,
				"audio/ogg":               event.CapLevelFullySupported,
				"audio/webm; codecs=opus": event.CapLevelFullySupported,
				"audio/webm":              event.CapLevelFullySupported,
				"audio/*":                 event.CapLevelPartialSupport,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgGIF: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/gif": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
	},
	LocationMessage: event.CapLevelUnsupported,
	MaxTextLength:   MaxTextLength,
	Thread:          event.CapLevelPartialSupport,
}

func (d *DiscordClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	if portal.Metadata.(*discordid.PortalMetadata).GuildID == "" {
		caps := discordCaps.Clone()
		caps.Thread = event.CapLevelUnsupported
		return caps
	}
	return discordCaps
}
