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

package msgconv

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
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

// renderDiscordMarkdownOnlyHTML converts Discord-flavored Markdown text to HTML.
//
// After conversion, if the text is surrounded by a single outermost paragraph
// tag, it is unwrapped.
func (mc *MessageConverter) renderDiscordMarkdownOnlyHTML(portal *bridgev2.Portal, text string, allowInlineLinks bool) string {
	return format.UnwrapSingleParagraph(mc.renderDiscordMarkdownOnlyHTMLNoUnwrap(portal, text, allowInlineLinks))
}

// renderDiscordMarkdownOnlyHTMLNoUnwrap converts Discord-flavored Markdown text to HTML.
func (mc *MessageConverter) renderDiscordMarkdownOnlyHTMLNoUnwrap(portal *bridgev2.Portal, text string, allowInlineLinks bool) string {
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

const formatterContextPortalKey = "fi.mau.discord.portal"
const formatterContextAllowedMentionsKey = "fi.mau.discord.allowed_mentions"
const formatterContextInputAllowedMentionsKey = "fi.mau.discord.input_allowed_mentions"
const formatterContextInputAllowedLinkPreviewsKey = "fi.mau.discord.input_allowed_link_previews"

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
