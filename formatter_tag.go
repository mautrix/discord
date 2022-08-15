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
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix"

	"go.mau.fi/mautrix-discord/database"
)

type astDiscordTag struct {
	ast.BaseInline
	id int64
}

var _ ast.Node = (*astDiscordTag)(nil)
var astKindDiscordTag = ast.NewNodeKind("DiscordTag")

func (n *astDiscordTag) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *astDiscordTag) Kind() ast.NodeKind {
	return astKindDiscordTag
}

type astDiscordUserMention struct {
	astDiscordTag
	hasNick bool
}

func (n *astDiscordUserMention) String() string {
	if n.hasNick {
		return fmt.Sprintf("<@!%d>", n.id)
	}
	return fmt.Sprintf("<@%d>", n.id)
}

type astDiscordRoleMention struct {
	astDiscordTag
}

func (n *astDiscordRoleMention) String() string {
	return fmt.Sprintf("<@&%d>", n.id)
}

type astDiscordChannelMention struct {
	astDiscordTag

	guildID int64
	name    string
}

func (n *astDiscordChannelMention) String() string {
	if n.guildID != 0 {
		return fmt.Sprintf("<#%d:%d:%s>", n.id, n.guildID, n.name)
	}
	return fmt.Sprintf("<#%d>", n.id)
}

type discordTimestampStyle rune

func (dts discordTimestampStyle) Format() string {
	switch dts {
	case 't':
		return "15:04 MST"
	case 'T':
		return "15:04:05 MST"
	case 'd':
		return "2006-01-02 MST"
	case 'D':
		return "2 January 2006 MST"
	case 'F':
		return "Monday, 2 January 2006 15:04 MST"
	case 'f':
		fallthrough
	default:
		return "2 January 2006 15:04 MST"
	}
}

type astDiscordTimestamp struct {
	astDiscordTag

	timestamp int64
	style     discordTimestampStyle
}

func (n *astDiscordTimestamp) String() string {
	if n.style == 'f' {
		return fmt.Sprintf("<t:%d>", n.timestamp)
	}
	return fmt.Sprintf("<t:%d:%c>", n.timestamp, n.style)
}

type astDiscordCustomEmoji struct {
	astDiscordTag
	name     string
	animated bool
}

func (n *astDiscordCustomEmoji) String() string {
	if n.animated {
		return fmt.Sprintf("<a%s%d>", n.name, n.id)
	}
	return fmt.Sprintf("<%s%d>", n.name, n.id)
}

type discordTagParser struct{}

// Regex to match everything in https://discord.com/developers/docs/reference#message-formatting
var discordTagRegex = regexp.MustCompile(`<(a?:\w+:|@[!&]?|#|t:)(\d+)(?::([tTdDfFR])|(\d+):(.+?))?>`)
var defaultDiscordTagParser = &discordTagParser{}

func (s *discordTagParser) Trigger() []byte {
	return []byte{'<'}
}

func (s *discordTagParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	//before := block.PrecendingCharacter()
	line, _ := block.PeekLine()
	match := discordTagRegex.FindSubmatch(line)
	if match == nil {
		return nil
	}
	//seg := segment.WithStop(segment.Start + len(match[0]))
	block.Advance(len(match[0]))

	id, err := strconv.ParseInt(string(match[2]), 10, 64)
	if err != nil {
		return nil
	}
	tag := astDiscordTag{id: id}
	tagName := string(match[1])
	switch {
	case tagName == "@":
		return &astDiscordUserMention{astDiscordTag: tag}
	case tagName == "@!":
		return &astDiscordUserMention{astDiscordTag: tag, hasNick: true}
	case tagName == "@&":
		return &astDiscordRoleMention{astDiscordTag: tag}
	case tagName == "#":
		var guildID int64
		var channelName string
		if len(match[4]) > 0 && len(match[5]) > 0 {
			guildID, _ = strconv.ParseInt(string(match[4]), 10, 64)
			channelName = string(match[5])
		}
		return &astDiscordChannelMention{astDiscordTag: tag, guildID: guildID, name: channelName}
	case tagName == "t:":
		var style discordTimestampStyle
		if len(match[3]) == 0 {
			style = 'f'
		} else {
			style = discordTimestampStyle(match[3][0])
		}
		return &astDiscordTimestamp{
			astDiscordTag: tag,
			timestamp:     id,
			style:         style,
		}
	case strings.HasPrefix(tagName, ":"):
		return &astDiscordCustomEmoji{name: tagName, astDiscordTag: tag}
	case strings.HasPrefix(tagName, "a:"):
		return &astDiscordCustomEmoji{name: tagName[1:], astDiscordTag: tag}
	default:
		return nil
	}
}

func (s *discordTagParser) CloseBlock(parent ast.Node, pc parser.Context) {
	// nothing to do
}

type discordTagHTMLRenderer struct {
	portal *Portal
}

func (r *discordTagHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(astKindDiscordTag, r.renderDiscordMention)
}

func relativeTimeFormat(ts time.Time) string {
	now := time.Now()
	if ts.Year() >= 2262 {
		return "date out of range for relative format"
	}
	duration := ts.Sub(now)
	word := "in %s"
	if duration < 0 {
		duration = -duration
		word = "%s ago"
	}
	var count int
	var unit string
	switch {
	case duration < time.Second:
		count = int(duration.Milliseconds())
		unit = "millisecond"
	case duration < time.Minute:
		count = int(math.Round(duration.Seconds()))
		unit = "second"
	case duration < time.Hour:
		count = int(math.Round(duration.Minutes()))
		unit = "minute"
	case duration < 24*time.Hour:
		count = int(math.Round(duration.Hours()))
		unit = "hour"
	case duration < 30*24*time.Hour:
		count = int(math.Round(duration.Hours() / 24))
		unit = "day"
	case duration < 365*24*time.Hour:
		count = int(math.Round(duration.Hours() / 24 / 30))
		unit = "month"
	default:
		count = int(math.Round(duration.Hours() / 24 / 365))
		unit = "year"
	}
	var diff string
	if count == 1 {
		diff = fmt.Sprintf("a %s", unit)
	} else {
		diff = fmt.Sprintf("%d %ss", count, unit)
	}
	return fmt.Sprintf(word, diff)
}

func (r *discordTagHTMLRenderer) renderDiscordMention(w util.BufWriter, source []byte, n ast.Node, entering bool) (status ast.WalkStatus, err error) {
	status = ast.WalkContinue
	if !entering {
		return
	}
	switch node := n.(type) {
	case *astDiscordUserMention:
		puppet := r.portal.bridge.GetPuppetByID(strconv.FormatInt(node.id, 10))
		_, _ = fmt.Fprintf(w, `<a href="https://matrix.to/#/%s">%s</a>`, puppet.MXID, puppet.Name)
		return
	case *astDiscordRoleMention:
		role := r.portal.bridge.DB.Role.GetByID(r.portal.GuildID, strconv.FormatInt(node.id, 10))
		if role != nil {
			_, _ = fmt.Fprintf(w, `<font color="#%06x"><strong>@%s</strong></font>`, role.Color, role.Name)
			return
		}
	case *astDiscordChannelMention:
		portal := r.portal.bridge.GetExistingPortalByID(database.PortalKey{
			ChannelID: strconv.FormatInt(node.id, 10),
			Receiver:  "",
		})
		if portal != nil {
			if portal.MXID != "" {
				_, _ = fmt.Fprintf(w, `<a href="https://matrix.to/#/%s?via=%s">%s</a>`, portal.MXID, portal.bridge.AS.HomeserverDomain, portal.Name)
			} else {
				_, _ = w.WriteString(portal.Name)
			}
			return
		}
	case *astDiscordCustomEmoji:
		reactionMXC := r.portal.getEmojiMXCByDiscordID(strconv.FormatInt(node.id, 10), node.name, node.animated)
		if !reactionMXC.IsEmpty() {
			_, _ = fmt.Fprintf(w, `<img data-mx-emoticon src="%[1]s" alt="%[2]s" title="%[2]s" height="32"/>`, reactionMXC.String(), node.name)
			return
		}
	case *astDiscordTimestamp:
		ts := time.Unix(node.timestamp, 0).UTC()
		var formatted string
		if node.style == 'R' {
			formatted = relativeTimeFormat(ts)
		} else {
			formatted = ts.Format(node.style.Format())
		}
		// https://github.com/matrix-org/matrix-spec-proposals/pull/3160
		const fullDatetimeFormat = "2006-01-02T15:04:05.000-0700"
		fullRFC := ts.Format(fullDatetimeFormat)
		fullHumanReadable := ts.Format(discordTimestampStyle('F').Format())
		_, _ = fmt.Fprintf(w, `<time title="%s" datetime="%s"><strong>%s</strong></time>`, fullHumanReadable, fullRFC, formatted)
	}
	stringifiable, ok := n.(mautrix.Stringifiable)
	if ok {
		_, _ = w.WriteString(stringifiable.String())
	} else {
		_, _ = w.Write(source)
	}
	return
}

type DiscordTag struct {
	Portal *Portal
}

func (e *DiscordTag) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(defaultDiscordTagParser, 600),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&discordTagHTMLRenderer{e.Portal}, 600),
	))
}
