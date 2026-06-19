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

package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// newTestRenderContext builds a render context with no bridge wiring. Mention
// resolution is nil-safe, so pure-markdown rendering works without a connector.
func newTestRenderContext(roles map[string]*cachedRole) *discordRenderContext {
	return &discordRenderContext{
		ctx:   context.Background(),
		roles: roles,
	}
}

func TestRenderMarkdown_PlainAndFormatting(t *testing.T) {
	rctx := newTestRenderContext(nil)
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain", "hello world", "hello world"},
		{"bold", "**bold**", "<strong>bold</strong>"},
		{"italic", "*italic*", "<em>italic</em>"},
		{"strikethrough", "~~struck~~", "<del>struck</del>"},
		{"inline code", "`code`", "<code>code</code>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rctx.renderMarkdown(test.input, true)
			if got != test.expected {
				t.Errorf("renderMarkdown(%q) = %q, want %q", test.input, got, test.expected)
			}
		})
	}
}

func TestRenderMarkdown_Everyone(t *testing.T) {
	rctx := newTestRenderContext(nil)
	got := rctx.renderMarkdown("@everyone hello", true)
	if !strings.Contains(got, `<span class="discord-mention-everyone">@room</span>`) {
		t.Errorf("@everyone not rendered to @room span: %q", got)
	}
	here := rctx.renderMarkdown("@here hello", true)
	if !strings.Contains(here, `<span class="discord-mention-here">@room</span>`) {
		t.Errorf("@here not rendered to @room span: %q", here)
	}
}

func TestRenderMarkdown_RoleMention(t *testing.T) {
	rctx := newTestRenderContext(map[string]*cachedRole{
		"123": {Name: "Admins", Color: 0xff0000},
	})
	got := rctx.renderMarkdown("<@&123>", true)
	want := `<font color="#ff0000"><strong>@Admins</strong></font>`
	if got != want {
		t.Errorf("role mention = %q, want %q", got, want)
	}
	// Unknown role falls back to the raw tag string (the renderer writes the
	// node's String() verbatim, matching legacy behavior).
	unknown := rctx.renderMarkdown("<@&999>", true)
	if unknown != "<@&999>" {
		t.Errorf("unknown role fallback = %q", unknown)
	}
}

func TestRenderMarkdown_Timestamp(t *testing.T) {
	rctx := newTestRenderContext(nil)
	// 2021-01-01T00:00:00Z = 1609459200
	got := rctx.renderMarkdown("<t:1609459200:D>", true)
	if !strings.Contains(got, `data-discord-style="D"`) {
		t.Errorf("timestamp style not rendered: %q", got)
	}
	if !strings.Contains(got, "1 January 2021") {
		t.Errorf("timestamp not formatted: %q", got)
	}
}

// TestAntiPingInjection verifies the FR-69 anti-ping zero-width form replaces a
// rendered @room when the message isn't permitted to ping everyone.
func TestAntiPingInjection(t *testing.T) {
	rctx := newTestRenderContext(nil)
	rendered := rctx.renderMarkdown("@everyone", true)
	// Simulate the convertTextMessage gate: not permitted → replace @room.
	gated := strings.ReplaceAll(rendered, "@room", antiPingRoom)
	if strings.Contains(gated, ">@room<") {
		t.Errorf("anti-ping not applied: %q", gated)
	}
	if !strings.Contains(gated, antiPingRoom) {
		t.Errorf("anti-ping form missing: %q", gated)
	}
}

func TestGetEmbedType(t *testing.T) {
	tests := []struct {
		name  string
		embed *discordgo.MessageEmbed
		msg   *discordgo.Message
		want  BridgeEmbedType
	}{
		{"link", &discordgo.MessageEmbed{Type: discordgo.EmbedTypeLink}, nil, EmbedLinkPreview},
		{"article", &discordgo.MessageEmbed{Type: discordgo.EmbedTypeArticle}, nil, EmbedLinkPreview},
		{"rich", &discordgo.MessageEmbed{Type: discordgo.EmbedTypeRich}, nil, EmbedRich},
		{"gifv", &discordgo.MessageEmbed{Type: discordgo.EmbedTypeGifv}, nil, EmbedVideo},
		{
			"video with proxy",
			&discordgo.MessageEmbed{Type: discordgo.EmbedTypeVideo, Video: &discordgo.MessageEmbedVideo{ProxyURL: "https://x/y"}},
			nil, EmbedVideo,
		},
		{
			"video without proxy is link preview",
			&discordgo.MessageEmbed{Type: discordgo.EmbedTypeVideo, Video: &discordgo.MessageEmbedVideo{}},
			nil, EmbedLinkPreview,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := getEmbedType(test.msg, test.embed); got != test.want {
				t.Errorf("getEmbedType() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsPlainGifMessage(t *testing.T) {
	gifMsg := &discordgo.Message{
		Content: "https://tenor.com/view/x",
		Embeds: []*discordgo.MessageEmbed{{
			Type:  discordgo.EmbedTypeGifv,
			URL:   "https://tenor.com/view/x",
			Video: &discordgo.MessageEmbedVideo{ProxyURL: "https://media.tenor.com/x.mp4"},
		}},
	}
	if !isPlainGifMessage(gifMsg) {
		t.Error("expected plain gif message to be detected")
	}
	textMsg := &discordgo.Message{Content: "just text"}
	if isPlainGifMessage(textMsg) {
		t.Error("plain text wrongly detected as gif message")
	}
}

func TestAssignPartIDs(t *testing.T) {
	// Single part collapses to "".
	single := []*bridgev2.ConvertedMessagePart{
		{ID: "attachment-0-aid", Content: &event.MessageEventContent{MsgType: event.MsgImage}},
	}
	assignPartIDs(single)
	if single[0].ID != "" {
		t.Errorf("single part ID = %q, want empty", single[0].ID)
	}

	// Multi-part: text keeps "", attachment keeps its attachment-<idx>-<id> id.
	multi := []*bridgev2.ConvertedMessagePart{
		{Content: &event.MessageEventContent{MsgType: event.MsgText}},
		{ID: "attachment-0-aid", Content: &event.MessageEventContent{MsgType: event.MsgImage}},
	}
	assignPartIDs(multi)
	if multi[0].ID != "" {
		t.Errorf("text part ID = %q, want empty", multi[0].ID)
	}
	if multi[1].ID != "attachment-0-aid" {
		t.Errorf("attachment part ID = %q, want attachment-0-aid", multi[1].ID)
	}
}
