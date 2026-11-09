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
	"context"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
)

type discordEscapeParser struct{}

func (s *discordEscapeParser) Trigger() []byte {
	return []byte{'\\'}
}

func (s *discordEscapeParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 4 || line[1] != line[2] || line[1] == line[3] || (line[1] != '_' && line[1] != '*') {
		return nil
	}
	block.Advance(3)
	return ast.NewString(line[1:3])
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
var discordParserOptions = goldmark.WithParserOptions(
	parser.WithBlockParsers(util.Prioritized(defaultIndentableParagraphParser, 500)),
	parser.WithInlineParsers(util.Prioritized(&discordEscapeParser{}, 100)),
)
var discordExtensions = goldmark.WithExtensions(extension.Strikethrough, mdext.SimpleSpoiler, mdext.DiscordUnderline, ExtDiscordEveryone, ExtDiscordTag)

var discordRenderer = goldmark.New(
	goldmark.WithParser(mdext.ParserWithoutFeatures(removeFeaturesAndLinks...)),
	discordParserOptions, format.HTMLOptions, discordExtensions,
)
var discordRendererWithInlineLinks = goldmark.New(
	goldmark.WithParser(mdext.ParserWithoutFeatures(removeFeaturesExceptLinks...)),
	discordParserOptions, format.HTMLOptions, discordExtensions,
)

// renderDiscordMarkdownOnlyHTML converts Discord-flavored Markdown text to HTML.
//
// After conversion, if the text is surrounded by a single outermost paragraph
// tag, it is unwrapped.
func (mc *MessageConverter) renderDiscordMarkdownOnlyHTML(ctx context.Context, portal *bridgev2.Portal, source *bridgev2.UserLogin, text string, allowInlineLinks bool) string {
	return format.UnwrapSingleParagraph(mc.renderDiscordMarkdownOnlyHTMLNoUnwrap(ctx, portal, source, text, allowInlineLinks))
}

// renderDiscordMarkdownOnlyHTMLNoUnwrap converts Discord-flavored Markdown text to HTML.
func (mc *MessageConverter) renderDiscordMarkdownOnlyHTMLNoUnwrap(ctx context.Context, portal *bridgev2.Portal, source *bridgev2.UserLogin, text string, allowInlineLinks bool) string {
	var buf strings.Builder
	pc := parser.NewContext()
	pc.Set(parserContextGoContext, ctx)
	pc.Set(parserContextPortal, portal)
	pc.Set(parserContextUserLogin, source)
	renderer := discordRenderer
	if allowInlineLinks {
		renderer = discordRendererWithInlineLinks
	}
	err := renderer.Convert([]byte(text), &buf, parser.WithContext(pc))
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
