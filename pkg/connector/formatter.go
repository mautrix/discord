// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2023 Tulir Asokan
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

// Ported from legacy formatter.go into pkg/connector (ar M2). This file holds
// the Discord-markdown → Matrix-HTML goldmark pipeline plus the
// discordRenderContext that carries the bridgev2 lookups (mentions, roles,
// channels, custom emoji) the renderer needs.
//
// NOTE: the Matrix→Discord direction (parseMatrixHTML / escapeDiscordMarkdown /
// matrixHTMLParser) is NOT ported here — that belongs to convertmatrix.go
// (Group 5 / Task 5.1).
package connector

import (
	"context"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// escapeFixer is a hacky partial fix for the difference in escaping markdown, used with escapeReplacement
//
// Discord allows escaping with just one backslash, e.g. \__a__,
// but standard markdown requires both to be escaped (\_\_a__)
var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func escapeReplacement(s string) string {
	return s[:2] + `\` + s[2:]
}

// indentableParagraphParser is the default paragraph parser with CanAcceptIndentedLine.
// Used when disabling CodeBlockParser (as disabling it without a replacement will make indented blocks disappear).
type indentableParagraphParser struct {
	parser.BlockParser
}

var defaultIndentableParagraphParser = &indentableParagraphParser{BlockParser: parser.NewParagraphParser()}

func (b *indentableParagraphParser) CanAcceptIndentedLine() bool {
	return true
}

var removeFeaturesExceptLinks = []any{
	parser.NewListParser(), parser.NewListItemParser(), parser.NewHTMLBlockParser(), parser.NewRawHTMLParser(),
	parser.NewSetextHeadingParser(), parser.NewThematicBreakParser(),
	parser.NewCodeBlockParser(),
}
var removeFeaturesAndLinks = append(removeFeaturesExceptLinks, parser.NewLinkParser())
var fixIndentedParagraphs = goldmark.WithParserOptions(parser.WithBlockParsers(util.Prioritized(defaultIndentableParagraphParser, 500)))
var discordExtensions = goldmark.WithExtensions(extension.Strikethrough, mdext.SimpleSpoiler, mdext.DiscordUnderline, ExtDiscordEveryone, ExtDiscordTag)

var discordRenderer = goldmark.New(
	goldmark.WithParser(mdext.ParserWithoutFeatures(removeFeaturesAndLinks...)),
	fixIndentedParagraphs, format.HTMLOptions, discordExtensions,
)
var discordRendererWithInlineLinks = goldmark.New(
	goldmark.WithParser(mdext.ParserWithoutFeatures(removeFeaturesExceptLinks...)),
	fixIndentedParagraphs, format.HTMLOptions, discordExtensions,
)

// parserContextRender is the goldmark parser-context key under which the active
// *discordRenderContext is stored. The tag/emoji/role renderers read it back to
// resolve mentions against the bridgev2 framework. (The legacy code stored a
// *Portal here; bridgev2 has no per-portal renderer, so we store the richer
// render context.)
var parserContextRender = parser.NewContextKey()

// discordRenderContext carries everything the goldmark mention renderers need to
// turn Discord tags into Matrix HTML. It is constructed per message (cheap) by
// convertdiscord.go and stored in the goldmark parser.Context.
//
// resolveCustomEmoji may upload media (and therefore needs ctx); the renderers
// run synchronously inside goldmark.Convert, so ctx is captured here.
type discordRenderContext struct {
	ctx              context.Context
	conn             *DiscordConnector
	portal           *bridgev2.Portal
	guildID          string
	homeserverDomain string

	// roles is the in-memory role cache for this guild (H10), keyed by role ID.
	// It is populated by convertdiscord.go from the connector role cache /
	// dc_role backing store before rendering, so the renderer never blocks on a
	// DB query in the hot path.
	roles map[string]*cachedRole
}

// cachedRole is the minimal role data the renderer needs.
type cachedRole struct {
	Name  string
	Color int
}

// renderMarkdownNoUnwrap converts Discord markdown to Matrix HTML. allowInlineLinks
// controls whether [text](url) links are parsed (matches legacy
// renderDiscordMarkdownOnlyHTML / ...NoUnwrap). The render context resolves
// mentions; pass nil to render without mention resolution (mentions fall back to
// their raw <@id> form).
func (rctx *discordRenderContext) renderMarkdownNoUnwrap(text string, allowInlineLinks bool) string {
	text = escapeFixer.ReplaceAllStringFunc(text, escapeReplacement)

	var buf strings.Builder
	pc := parser.NewContext()
	pc.Set(parserContextRender, rctx)
	renderer := discordRenderer
	if allowInlineLinks {
		renderer = discordRendererWithInlineLinks
	}
	err := renderer.Convert([]byte(text), &buf, parser.WithContext(pc))
	if err != nil {
		// The legacy code panicked here; a malformed message must not crash the
		// whole convert path in bridgev2, so log and fall back to the raw text.
		if rctx != nil && rctx.ctx != nil {
			zerolog.Ctx(rctx.ctx).Err(err).Msg("Discord markdown parser errored")
		}
		return text
	}
	return buf.String()
}

func (rctx *discordRenderContext) renderMarkdown(text string, allowInlineLinks bool) string {
	return format.UnwrapSingleParagraph(rctx.renderMarkdownNoUnwrap(text, allowInlineLinks))
}

// --- mention resolution (bridgev2 rewiring of the legacy DB/bridge lookups) ---

// resolveUserMention returns the Matrix mention target for a Discord user ID. If
// the user is a logged-in Matrix user, the pill points at their real MXID;
// otherwise it points at the ghost. Returns an empty MXID if it can't be
// resolved (the renderer then falls back to the raw <@id>).
func (rctx *discordRenderContext) resolveUserMention(userID string) (id.UserID, string) {
	if rctx.conn == nil || rctx.conn.br == nil {
		return "", ""
	}
	br := rctx.conn.br
	// Prefer the real Matrix user if this Discord user is logged in.
	if login, err := br.GetExistingUserLoginByID(rctx.ctx, discordid.MakeUserLoginID(userID)); err == nil && login != nil {
		name := login.UserMXID.Localpart()
		if ghost, gerr := br.GetGhostByID(rctx.ctx, discordid.MakeUserID(userID)); gerr == nil && ghost != nil && ghost.Name != "" {
			name = ghost.Name
		}
		return login.UserMXID, name
	}
	ghost, err := br.GetGhostByID(rctx.ctx, discordid.MakeUserID(userID))
	if err != nil || ghost == nil {
		return "", ""
	}
	mxid := ghost.Intent.GetMXID()
	name := ghost.Name
	if name == "" {
		name = mxid.Localpart()
	}
	return mxid, name
}

// resolveRoleMention returns the name+color for a Discord role ID from the
// per-message role cache. The cache is the hot path (ar H10); convertdiscord.go
// populates it from the connector's in-memory role cache with a dc_role
// cold-start fallback.
func (rctx *discordRenderContext) resolveRoleMention(roleID string) (name string, color int, ok bool) {
	if rctx.roles == nil {
		return "", 0, false
	}
	role, found := rctx.roles[roleID]
	if !found || role == nil {
		return "", 0, false
	}
	return role.Name, role.Color, true
}

// resolveChannelMention returns the portal MXID (may be empty) and name for a
// Discord channel ID. The legacy code only resolved channels with an empty
// receiver (guild channels); DMs aren't mentionable in Discord text, so the
// same restriction applies.
func (rctx *discordRenderContext) resolveChannelMention(channelID string) (mxid id.RoomID, name string, ok bool) {
	if rctx.conn == nil || rctx.conn.br == nil {
		return "", "", false
	}
	portal, err := rctx.conn.br.GetExistingPortalByKey(rctx.ctx, discordid.MakePortalKey(channelID, "", false))
	if err != nil || portal == nil {
		return "", "", false
	}
	return portal.MXID, channelMentionName(portal), true
}

// channelMentionName derives a display name for a channel mention. bridgev2
// portals store the room name (with the channel_name template applied); strip a
// leading '#' so we don't double it in the "#name" rendering below — the legacy
// PlainName had no prefix.
func channelMentionName(portal *bridgev2.Portal) string {
	name := portal.Name
	name = strings.TrimPrefix(name, "#")
	if name == "" {
		name = string(discordid.ParsePortalID(portal.ID))
	}
	return name
}

// resolveCustomEmoji returns the mxc:// URI for a Discord custom emoji, uploading
// it to Matrix (and caching in dc_file) if necessary. Returns an empty URI on
// failure (the renderer then falls back to the raw emoji text).
func (rctx *discordRenderContext) resolveCustomEmoji(emojiID, name string, animated bool) id.ContentURI {
	if rctx.conn == nil {
		return id.ContentURI{}
	}
	return rctx.conn.resolveCustomEmojiMXC(rctx.ctx, rctx.portal, emojiID, name, animated)
}

const discordLinkPattern = `https?://[^<\p{Zs}\x{feff}]*[^"'),.:;\]\p{Zs}\x{feff}]`

// Discord links start with http:// or https://, contain at least two characters afterwards,
// don't contain < or whitespace anywhere, and don't end with "'),.:;]
//
// Zero-width whitespace is mostly in the Format category and is allowed, except  isn't for some reason
var discordLinkRegex = regexp.MustCompile(discordLinkPattern)
var discordLinkRegexFull = regexp.MustCompile("^" + discordLinkPattern + "$")

// hackyReplyPattern matches the legacy "Replying to" embed that older Discord
// clients emit instead of a MessageReference (ported from legacy portal.go).
var hackyReplyPattern = regexp.MustCompile(`^\*\*\[Replying to]\(https://discord.com/channels/(\d+)/(\d+)/(\d+)\)`)

func isReplyEmbed(embed *discordgo.MessageEmbed) bool {
	return hackyReplyPattern.MatchString(embed.Description)
}
