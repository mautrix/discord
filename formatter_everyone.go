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

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type astDiscordEveryone struct {
	ast.BaseInline
	onlyHere bool
}

var _ ast.Node = (*astDiscordEveryone)(nil)
var astKindDiscordEveryone = ast.NewNodeKind("DiscordEveryone")

func (n *astDiscordEveryone) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *astDiscordEveryone) Kind() ast.NodeKind {
	return astKindDiscordEveryone
}

func (n *astDiscordEveryone) String() string {
	if n.onlyHere {
		return "@here"
	}
	return "@everyone"
}

type discordEveryoneParser struct{}

var discordEveryoneRegex = regexp.MustCompile(`@(everyone|here)`)
var defaultDiscordEveryoneParser = &discordEveryoneParser{}

func (s *discordEveryoneParser) Trigger() []byte {
	return []byte{'@'}
}

func (s *discordEveryoneParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	match := discordEveryoneRegex.FindSubmatch(line)
	if match == nil {
		return nil
	}
	block.Advance(len(match[0]))
	return &astDiscordEveryone{
		onlyHere: string(match[1]) == "here",
	}
}

func (s *discordEveryoneParser) CloseBlock(parent ast.Node, pc parser.Context) {
	// nothing to do
}

type discordEveryoneHTMLRenderer struct{}

func (r *discordEveryoneHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(astKindDiscordEveryone, r.renderDiscordEveryone)
}

func (r *discordEveryoneHTMLRenderer) renderDiscordEveryone(w util.BufWriter, source []byte, n ast.Node, entering bool) (status ast.WalkStatus, err error) {
	status = ast.WalkContinue
	if !entering {
		return
	}
	mention, _ := n.(*astDiscordEveryone)
	class := "everyone"
	if mention != nil && mention.onlyHere {
		class = "here"
	}
	_, _ = fmt.Fprintf(w, `<span class="discord-mention-%s">@room</span>`, class)
	return
}

type discordEveryone struct{}

var ExtDiscordEveryone = &discordEveryone{}

func (e *discordEveryone) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(defaultDiscordEveryoneParser, 600),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&discordEveryoneHTMLRenderer{}, 600),
	))
}
