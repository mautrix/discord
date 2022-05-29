package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
)

var mdRenderer = goldmark.New(format.Extensions, format.HTMLOptions,
	goldmark.WithExtensions(mdext.EscapeHTML, mdext.SimpleSpoiler, mdext.DiscordUnderline))
var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func renderDiscordMarkdown(text string) event.MessageEventContent {
	text = escapeFixer.ReplaceAllStringFunc(text, func(s string) string {
		return s[:2] + `\` + s[2:]
	})
	return format.RenderMarkdownCustom(text, mdRenderer)
}

var matrixHTMLParser = &format.HTMLParser{
	PillConverter:  nil,
	TabsToSpaces:   4,
	Newline:        "\n",
	HorizontalLine: "\n---\n",
	ItalicConverter: func(s string, context format.Context) string {
		return fmt.Sprintf("*%s*", s)
	},
	UnderlineConverter: func(s string, context format.Context) string {
		return fmt.Sprintf("__%s__", s)
	},
	TextConverter: func(s string, context format.Context) string {
		return discordMarkdownEscaper.Replace(s)
	},
	SpoilerConverter: func(text, reason string, ctx format.Context) string {
		if reason != "" {
			return fmt.Sprintf("(%s) ||%s||", reason, text)
		}
		return fmt.Sprintf("||%s||", text)
	},
}

var discordMarkdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`_`, `\_`,
	`*`, `\*`,
	`~`, `\~`,
	"`", "\\`",
	`|`, `\|`,
)

func parseMatrixHTML(content *event.MessageEventContent) string {
	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		return matrixHTMLParser.Parse(content.FormattedBody, make(format.Context))
	} else {
		return discordMarkdownEscaper.Replace(content.Body)
	}
}
