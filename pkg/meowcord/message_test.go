// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright 2015-2016 Bruce Marriner <bruce@sqls.net>.  All rights reserved.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This file is derived from discordgo (https://github.com/bwmarrin/discordgo),
// used under the BSD-3-Clause license; see README.md in this directory. This
// file is distributed under the GNU AGPLv3 as follows:
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

package meowcord

import (
	"testing"
)

func TestContentWithMoreMentionsReplaced(t *testing.T) {
	s := &Session{StateEnabled: true, State: NewState()}

	user := &User{
		ID:       "user",
		Username: "User Name",
	}

	s.State.GuildAdd(&Guild{ID: "guild"})
	s.State.RoleAdd("guild", &Role{
		ID:          "role",
		Name:        "Role Name",
		Mentionable: true,
	})
	s.State.MemberAdd(&Member{
		User:    user,
		Nick:    "User Nick",
		GuildID: "guild",
	})
	s.State.ChannelAdd(&Channel{
		Name:    "Channel Name",
		GuildID: "guild",
		ID:      "channel",
	})
	m := &Message{
		Content:      "<@&role> <@!user> <@user> <#channel>",
		ChannelID:    "channel",
		MentionRoles: []string{"role"},
		Mentions:     []*User{user},
	}
	if result, _ := m.ContentWithMoreMentionsReplaced(s); result != "@Role Name @User Nick @User Name #Channel Name" {
		t.Error(result)
	}
}
func TestGettingEmojisFromMessage(t *testing.T) {
	msg := "test test <:kitty14:811736565172011058> <:kitty4:811736468812595260>"
	m := &Message{
		Content: msg,
	}
	emojis := m.GetCustomEmojis()
	if len(emojis) < 1 {
		t.Error("No emojis found.")
		return
	}

}

func TestMessage_Reference(t *testing.T) {
	m := &Message{
		ID:        "811736565172011001",
		GuildID:   "811736565172011002",
		ChannelID: "811736565172011003",
	}

	ref := m.Reference()

	if ref.Type != 0 {
		t.Error("Default reference type should be 0")
	}

	if ref.MessageID != m.ID {
		t.Error("Message ID should be the same")
	}

	if ref.GuildID != m.GuildID {
		t.Error("Guild ID should be the same")
	}

	if ref.ChannelID != m.ChannelID {
		t.Error("Channel ID should be the same")
	}
}

func TestMessage_Forward(t *testing.T) {
	m := &Message{
		ID:        "811736565172011001",
		GuildID:   "811736565172011002",
		ChannelID: "811736565172011003",
	}

	ref := m.Forward()

	if ref.Type != MessageReferenceTypeForward {
		t.Error("Reference type should be 1 (forward)")
	}

	if ref.MessageID != m.ID {
		t.Error("Message ID should be the same")
	}

	if ref.GuildID != m.GuildID {
		t.Error("Guild ID should be the same")
	}

	if ref.ChannelID != m.ChannelID {
		t.Error("Channel ID should be the same")
	}
}

func TestMessageReference_DefaultTypeIsDefault(t *testing.T) {
	r := MessageReference{}
	if r.Type != MessageReferenceTypeDefault {
		t.Error("Default message type should be MessageReferenceTypeDefault")
	}
}
