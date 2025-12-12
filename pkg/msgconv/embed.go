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

package msgconv

import (
	"regexp"

	"github.com/bwmarrin/discordgo"
)

type BridgeEmbedType int

const (
	EmbedUnknown BridgeEmbedType = iota
	EmbedRich
	EmbedLinkPreview
	EmbedVideo
)

const discordLinkPattern = `https?://[^<\p{Zs}\x{feff}]*[^"'),.:;\]\p{Zs}\x{feff}]`

// Discord links start with http:// or https://, contain at least two characters afterwards,
// don't contain < or whitespace anywhere, and don't end with "'),.:;]
//
// Zero-width whitespace is mostly in the Format category and is allowed, except \uFEFF isn't for some reason
// FIXME(skip): This will be unused until we port `escapeDiscordMarkdown`.
// var discordLinkRegex = regexp.MustCompile(discordLinkPattern)
var discordLinkRegexFull = regexp.MustCompile("^" + discordLinkPattern + "$")

func isActuallyLinkPreview(embed *discordgo.MessageEmbed) bool {
	// Sending YouTube links creates a video embed, but we want to bridge it as a URL preview,
	// so this is a hacky way to detect those.
	return embed.Video != nil && embed.Video.ProxyURL == ""
}

// isPlainGifMessage returns whether a Discord message consists entirely of a
// link to a GIF-like animated image. A single embed must also be present on the
// message.
//
// This helps replicate Discord first-party client behavior, where the link is
// hidden when these same conditions are fulfilled.
func isPlainGifMessage(msg *discordgo.Message) bool {
	if len(msg.Embeds) != 1 {
		return false
	}
	embed := msg.Embeds[0]
	isGifVideo := embed.Type == discordgo.EmbedTypeGifv && embed.Video != nil
	isGifImage := embed.Type == discordgo.EmbedTypeImage && embed.Image == nil && embed.Thumbnail != nil && embed.Title == ""
	contentIsOnlyURL := msg.Content == embed.URL || discordLinkRegexFull.MatchString(msg.Content)
	return contentIsOnlyURL && (isGifVideo || isGifImage)
}

// getEmbedType determines how a Discord embed should be bridged to Matrix by
// returning a BridgeEmbedType.
func getEmbedType(msg *discordgo.Message, embed *discordgo.MessageEmbed) BridgeEmbedType {
	switch embed.Type {
	case discordgo.EmbedTypeLink, discordgo.EmbedTypeArticle:
		return EmbedLinkPreview
	case discordgo.EmbedTypeVideo:
		if isActuallyLinkPreview(embed) {
			return EmbedLinkPreview
		}
		return EmbedVideo
	case discordgo.EmbedTypeGifv:
		return EmbedVideo
	case discordgo.EmbedTypeImage:
		if msg != nil && isPlainGifMessage(msg) {
			return EmbedVideo
		} else if embed.Image == nil && embed.Thumbnail != nil {
			return EmbedLinkPreview
		}
		return EmbedRich
	case discordgo.EmbedTypeRich:
		return EmbedRich
	default:
		return EmbedUnknown
	}
}

var hackyReplyPattern = regexp.MustCompile(`^\*\*\[Replying to]\(https://discord.com/channels/(\d+)/(\d+)/(\d+)\)`)

func isReplyEmbed(embed *discordgo.MessageEmbed) bool {
	return hackyReplyPattern.MatchString(embed.Description)
}
