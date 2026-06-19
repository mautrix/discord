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

// Ported from legacy formatter_tag.go into pkg/connector (ar M2). This is the
// goldmark inline extension that renders Discord mentions/timestamps/custom
// emoji as Matrix HTML.
//
// The legacy renderer reached into *Portal/*DiscordBridge directly
// (GetPuppetByID / GetUserByID / DB.Role / GetExistingPortalByID /
// getEmojiMXCByDiscordID). In bridgev2 those lookups go through the connector +
// framework, and rendering happens synchronously inside goldmark, so all the
// state the renderer needs is carried in a *discordRenderContext stored in the
// goldmark parser.Context under parserContextRender.
package connector

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
)

type astDiscordTag struct {
	ast.BaseInline
	// rctx is captured at parse time (the parser has the parser.Context; the
	// renderer funcs don't), mirroring how legacy stored *Portal on the node.
	rctx *discordRenderContext
	id   int64
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
	line, _ := block.PeekLine()
	match := discordTagRegex.FindSubmatch(line)
	if match == nil {
		return nil
	}
	block.Advance(len(match[0]))

	id, err := strconv.ParseInt(string(match[2]), 10, 64)
	if err != nil {
		return nil
	}
	rctx, _ := pc.Get(parserContextRender).(*discordRenderContext)
	tag := astDiscordTag{id: id, rctx: rctx}
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
		return &astDiscordCustomEmoji{name: tagName[1:], astDiscordTag: tag, animated: true}
	default:
		return nil
	}
}

func (s *discordTagParser) CloseBlock(parent ast.Node, pc parser.Context) {
	// nothing to do
}

type discordTagHTMLRenderer struct{}

var defaultDiscordTagHTMLRenderer = &discordTagHTMLRenderer{}

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
		if node.rctx != nil {
			mxid, name := node.rctx.resolveUserMention(strconv.FormatInt(node.id, 10))
			if mxid != "" {
				_, _ = fmt.Fprintf(w, `<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), name)
				return
			}
		}
	case *astDiscordRoleMention:
		if node.rctx != nil {
			if name, color, ok := node.rctx.resolveRoleMention(strconv.FormatInt(node.id, 10)); ok {
				_, _ = fmt.Fprintf(w, `<font color="#%06x"><strong>@%s</strong></font>`, color, name)
				return
			}
		}
	case *astDiscordChannelMention:
		if node.rctx != nil {
			if mxid, name, ok := node.rctx.resolveChannelMention(strconv.FormatInt(node.id, 10)); ok {
				if mxid != "" {
					_, _ = fmt.Fprintf(w, `<a href="%s">%s</a>`, mxid.URI(node.rctx.homeserverDomain).MatrixToURL(), name)
				} else {
					_, _ = w.WriteString(name)
				}
				return
			}
		}
	case *astDiscordCustomEmoji:
		if node.rctx != nil {
			reactionMXC := node.rctx.resolveCustomEmoji(strconv.FormatInt(node.id, 10), node.name, node.animated)
			if !reactionMXC.IsEmpty() {
				attrs := "data-mx-emoticon"
				if node.animated {
					attrs += " data-mau-animated-emoji"
				}
				_, _ = fmt.Fprintf(w, `<img %[3]s src="%[1]s" alt="%[2]s" title="%[2]s" height="32"/>`, reactionMXC.String(), node.name, attrs)
				return
			}
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
		_, _ = fmt.Fprintf(w, `<time title="%s" datetime="%s" data-discord-style="%c"><strong>%s</strong></time>`, fullHumanReadable, fullRFC, node.style, formatted)
		return
	}
	stringifiable, ok := n.(fmt.Stringer)
	if ok {
		_, _ = w.WriteString(stringifiable.String())
	} else {
		_, _ = w.Write(source)
	}
	return
}

type discordTag struct{}

var ExtDiscordTag = &discordTag{}

func (e *discordTag) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(defaultDiscordTagParser, 600),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(defaultDiscordTagHTMLRenderer, 600),
	))
}
