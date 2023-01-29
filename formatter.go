// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/variationselector"
)

var discordExtensions = goldmark.WithExtensions(mdext.SimpleSpoiler, mdext.DiscordUnderline)
var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func (portal *Portal) renderDiscordMarkdown(text string) event.MessageEventContent {
	return format.HTMLToContent(portal.renderDiscordMarkdownOnlyHTML(text))
}

func (portal *Portal) renderDiscordMarkdownOnlyHTML(text string) string {
	text = escapeFixer.ReplaceAllStringFunc(text, func(s string) string {
		return s[:2] + `\` + s[2:]
	})

	mdRenderer := goldmark.New(
		goldmark.WithParser(mdext.ParserWithoutFeatures(
			parser.NewListParser(), parser.NewListItemParser(), parser.NewHTMLBlockParser(), parser.NewRawHTMLParser(),
		)),
		format.Extensions, format.HTMLOptions, discordExtensions,
		goldmark.WithExtensions(&DiscordTag{portal}),
	)

	var buf strings.Builder
	err := mdRenderer.Convert([]byte(text), &buf)
	if err != nil {
		panic(fmt.Errorf("markdown parser errored: %w", err))
	}
	return format.UnwrapSingleParagraph(buf.String())
}

const formatterContextUserKey = "fi.mau.discord.user"
const formatterContextPortalKey = "fi.mau.discord.portal"

func pillConverter(displayname, mxid, eventID string, ctx format.Context) string {
	if len(mxid) == 0 {
		return displayname
	}
	user := ctx.ReturnData[formatterContextUserKey].(*User)
	if mxid[0] == '#' {
		alias, err := user.bridge.Bot.ResolveAlias(id.RoomAlias(mxid))
		if err != nil {
			return displayname
		}
		mxid = alias.RoomID.String()
	}
	if mxid[0] == '!' {
		portal := user.bridge.GetPortalByMXID(id.RoomID(mxid))
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
			} else if msg := user.bridge.DB.Message.GetByMXID(portal.Key, id.EventID(eventID)); msg != nil {
				guildID := portal.GuildID
				if guildID == "" {
					guildID = "@me"
				}
				return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, msg.DiscordProtoChannelID(), msg.DiscordID)
			}
		}
	} else if mxid[0] == '@' {
		parsedID, ok := user.bridge.ParsePuppetMXID(id.UserID(mxid))
		if ok {
			return fmt.Sprintf("<@%s>", parsedID)
		}
		mentionedUser := user.bridge.GetUserByMXID(id.UserID(mxid))
		if mentionedUser != nil && mentionedUser.DiscordID != "" {
			return fmt.Sprintf("<@%s>", mentionedUser.DiscordID)
		}
	}
	return displayname
}

// Discord links start with http:// or https://, contain at least two characters afterwards,
// don't contain < or whitespace anywhere, and don't end with "'),.:;]
//
// Zero-width whitespace is mostly in the Format category and is allowed, except \uFEFF isn't for some reason
var discordLinkRegex = regexp.MustCompile(`https?://[^<\p{Zs}\x{feff}]*[^"'),.:;\]\p{Zs}\x{feff}]`)

var discordMarkdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`_`, `\_`,
	`*`, `\*`,
	`~`, `\~`,
	"`", "\\`",
	`|`, `\|`,
	`<`, `\<`,
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
}

func init() {
	matrixHTMLParser.PillConverter = pillConverter
}

func (portal *Portal) parseMatrixHTML(user *User, content *event.MessageEventContent) string {
	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		ctx := format.NewContext()
		ctx.ReturnData[formatterContextUserKey] = user
		ctx.ReturnData[formatterContextPortalKey] = portal
		return variationselector.Remove(matrixHTMLParser.Parse(content.FormattedBody, ctx))
	} else {
		return variationselector.Remove(escapeDiscordMarkdown(content.Body))
	}
}
