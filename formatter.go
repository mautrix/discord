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

package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"
	"go.mau.fi/util/variationselector"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
	"maunium.net/go/mautrix/id"
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

func (portal *Portal) renderDiscordMarkdownOnlyHTMLNoUnwrap(text string, allowInlineLinks bool) string {
	text = escapeFixer.ReplaceAllStringFunc(text, escapeReplacement)

	var buf strings.Builder
	ctx := parser.NewContext()
	ctx.Set(parserContextPortal, portal)
	renderer := discordRenderer
	if allowInlineLinks {
		renderer = discordRendererWithInlineLinks
	}
	err := renderer.Convert([]byte(text), &buf, parser.WithContext(ctx))
	if err != nil {
		panic(fmt.Errorf("markdown parser errored: %w", err))
	}
	return buf.String()
}

func (portal *Portal) renderDiscordMarkdownOnlyHTML(text string, allowInlineLinks bool) string {
	return format.UnwrapSingleParagraph(portal.renderDiscordMarkdownOnlyHTMLNoUnwrap(text, allowInlineLinks))
}

const formatterContextPortalKey = "fi.mau.discord.portal"
const formatterContextAllowedMentionsKey = "fi.mau.discord.allowed_mentions"
const formatterContextInputAllowedMentionsKey = "fi.mau.discord.input_allowed_mentions"
const formatterContextInputAllowedLinkPreviewsKey = "fi.mau.discord.input_allowed_link_previews"

func appendIfNotContains(arr []string, newItem string) []string {
	for _, item := range arr {
		if item == newItem {
			return arr
		}
	}
	return append(arr, newItem)
}

func (br *DiscordBridge) pillConverter(displayname, mxid, eventID string, ctx format.Context) string {
	if len(mxid) == 0 {
		return displayname
	}
	if mxid[0] == '#' {
		alias, err := br.Bot.ResolveAlias(id.RoomAlias(mxid))
		if err != nil {
			return displayname
		}
		mxid = alias.RoomID.String()
	}
	if mxid[0] == '!' {
		portal := br.GetPortalByMXID(id.RoomID(mxid))
		if portal != nil {
			if eventID == "" {
				//currentPortal := ctx[formatterContextPortalKey].(*Portal)
				return fmt.Sprintf("<#%s>", portal.Key.ChannelID)
				//if currentPortal.GuildID == portal.GuildID {
				//} else if portal.GuildID != "" {
				//	return fmt.Sprintf("<#%s:%s:%s>", portal.Key.ChannelID, portal.GuildID, portal.Name)
				//} else {
				//	// TODO is mentioning private channels possible at all?
				//}
			} else if msg := br.DB.Message.GetByMXID(portal.Key, id.EventID(eventID)); msg != nil {
				guildID := portal.GuildID
				if guildID == "" {
					guildID = "@me"
				}
				return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, msg.DiscordProtoChannelID(), msg.DiscordID)
			}
		}
	} else if mxid[0] == '@' {
		allowedMentions, _ := ctx.ReturnData[formatterContextInputAllowedMentionsKey].([]id.UserID)
		if allowedMentions != nil && !slices.Contains(allowedMentions, id.UserID(mxid)) {
			return displayname
		}
		mentions := ctx.ReturnData[formatterContextAllowedMentionsKey].(*discordgo.MessageAllowedMentions)
		parsedID, ok := br.ParsePuppetMXID(id.UserID(mxid))
		if ok {
			mentions.Users = appendIfNotContains(mentions.Users, parsedID)
			return fmt.Sprintf("<@%s>", parsedID)
		}
		mentionedUser := br.GetUserByMXID(id.UserID(mxid))
		if mentionedUser != nil && mentionedUser.DiscordID != "" {
			mentions.Users = appendIfNotContains(mentions.Users, mentionedUser.DiscordID)
			return fmt.Sprintf("<@%s>", mentionedUser.DiscordID)
		}
	}
	return displayname
}

const discordLinkPattern = `https?://[^<\p{Zs}\x{feff}]*[^"'),.:;\]\p{Zs}\x{feff}]`

// Discord links start with http:// or https://, contain at least two characters afterwards,
// don't contain < or whitespace anywhere, and don't end with "'),.:;]
//
// Zero-width whitespace is mostly in the Format category and is allowed, except \uFEFF isn't for some reason
var discordLinkRegex = regexp.MustCompile(discordLinkPattern)
var discordLinkRegexFull = regexp.MustCompile("^" + discordLinkPattern + "$")

var discordMarkdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`_`, `\_`,
	`*`, `\*`,
	`~`, `\~`,
	"`", "\\`",
	`|`, `\|`,
	`<`, `\<`,
	`#`, `\#`,
)

func escapeDiscordMarkdown(s string) string {
	submatches := discordLinkRegex.FindAllStringIndex(s, -1)
	if submatches == nil {
		return discordMarkdownEscaper.Replace(s)
	}
	var builder strings.Builder
	offset := 0
	for _, match := range submatches {
		start := match[0]
		end := match[1]
		builder.WriteString(discordMarkdownEscaper.Replace(s[offset:start]))
		builder.WriteString(s[start:end])
		offset = end
	}
	builder.WriteString(discordMarkdownEscaper.Replace(s[offset:]))
	return builder.String()
}

var matrixHTMLParser = &format.HTMLParser{
	TabsToSpaces:   4,
	Newline:        "\n",
	HorizontalLine: "\n---\n",
	ItalicConverter: func(s string, ctx format.Context) string {
		return fmt.Sprintf("*%s*", s)
	},
	UnderlineConverter: func(s string, ctx format.Context) string {
		return fmt.Sprintf("__%s__", s)
	},
	TextConverter: func(s string, ctx format.Context) string {
		if ctx.TagStack.Has("pre") || ctx.TagStack.Has("code") {
			// If we're in a code block, don't escape markdown
			return s
		}
		return escapeDiscordMarkdown(s)
	},
	SpoilerConverter: func(text, reason string, ctx format.Context) string {
		if reason != "" {
			return fmt.Sprintf("(%s) ||%s||", reason, text)
		}
		return fmt.Sprintf("||%s||", text)
	},
	LinkConverter: func(text, href string, ctx format.Context) string {
		linkPreviews := ctx.ReturnData[formatterContextInputAllowedLinkPreviewsKey].([]string)
		allowPreview := linkPreviews == nil || slices.Contains(linkPreviews, href)
		if text == href {
			if !allowPreview {
				return fmt.Sprintf("<%s>", text)
			}
			return text
		} else if !discordLinkRegexFull.MatchString(href) {
			return fmt.Sprintf("%s (%s)", escapeDiscordMarkdown(text), escapeDiscordMarkdown(href))
		} else if !allowPreview {
			return fmt.Sprintf("[%s](<%s>)", escapeDiscordMarkdown(text), href)
		} else {
			return fmt.Sprintf("[%s](%s)", escapeDiscordMarkdown(text), href)
		}
	},
}

func (portal *Portal) parseMatrixHTML(content *event.MessageEventContent, allowedLinkPreviews []string) (string, *discordgo.MessageAllowedMentions) {
	allowedMentions := &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		Users:       []string{},
		RepliedUser: true,
	}
	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		ctx := format.NewContext()
		ctx.ReturnData[formatterContextInputAllowedLinkPreviewsKey] = allowedLinkPreviews
		ctx.ReturnData[formatterContextPortalKey] = portal
		ctx.ReturnData[formatterContextAllowedMentionsKey] = allowedMentions
		if content.Mentions != nil {
			ctx.ReturnData[formatterContextInputAllowedMentionsKey] = content.Mentions.UserIDs
		}
		return variationselector.FullyQualify(matrixHTMLParser.Parse(content.FormattedBody, ctx)), allowedMentions
	} else {
		return variationselector.FullyQualify(escapeDiscordMarkdown(content.Body)), allowedMentions
	}
}
