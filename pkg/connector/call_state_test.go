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

package connector

import (
	"slices"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestDiscordCallStateVoiceLeaveEditsActiveCall(t *testing.T) {
	state := newDiscordCallState()
	state.UpdateFromMessage(discordCallStateTestMessage("msg", "dm", "caller", "self"))

	edits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "self", ChannelID: ""},
	})
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !slices.Equal(edits[0].Call.Participants, []string{"caller"}) {
		t.Fatalf("unexpected participants after leave: %v", edits[0].Call.Participants)
	}

	duplicateEdits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "self", ChannelID: ""},
	})
	if len(duplicateEdits) != 0 {
		t.Fatalf("expected duplicate leave to be ignored, got %d edits", len(duplicateEdits))
	}
}

func TestDiscordCallStateVoiceJoinEditsActiveCall(t *testing.T) {
	state := newDiscordCallState()
	state.UpdateFromMessage(discordCallStateTestMessage("msg", "dm", "caller"))

	edits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "self", ChannelID: "dm"},
	})
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !slices.Equal(edits[0].Call.Participants, []string{"caller", "self"}) {
		t.Fatalf("unexpected participants after join: %v", edits[0].Call.Participants)
	}

	duplicateEdits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "self", ChannelID: "dm"},
	})
	if len(duplicateEdits) != 0 {
		t.Fatalf("expected duplicate join to be ignored, got %d edits", len(duplicateEdits))
	}
}

func TestDiscordCallStateEndedCallRemovesActiveCall(t *testing.T) {
	state := newDiscordCallState()
	endedAt := time.Date(2026, 6, 15, 15, 30, 0, 0, time.UTC)
	ended := discordCallStateTestMessage("msg", "dm", "caller", "self")
	state.UpdateFromMessage(ended)
	ended.Call.EndedTimestamp = &endedAt
	state.UpdateFromMessage(ended)

	edits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "self", ChannelID: ""},
	})
	if len(edits) != 0 {
		t.Fatalf("expected ended call to ignore voice leave, got %d edits", len(edits))
	}
}

func TestDiscordCallStateVoiceLeaveUsesBeforeUpdateChannel(t *testing.T) {
	state := newDiscordCallState()
	state.UpdateFromMessage(discordCallStateTestMessage("msg", "dm", "caller", "self"))
	delete(state.voiceChannelByUser, "self")

	edits := state.UpdateFromVoiceState(&discordgo.VoiceStateUpdate{
		VoiceState:   &discordgo.VoiceState{UserID: "self", ChannelID: ""},
		BeforeUpdate: &discordgo.VoiceState{UserID: "self", ChannelID: "dm"},
	})
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !slices.Equal(edits[0].Call.Participants, []string{"caller"}) {
		t.Fatalf("unexpected participants after leave: %v", edits[0].Call.Participants)
	}
}

func discordCallStateTestMessage(messageID, channelID string, participants ...string) *discordgo.Message {
	return &discordgo.Message{
		ID:        messageID,
		ChannelID: channelID,
		Type:      discordgo.MessageTypeCall,
		Timestamp: time.Date(2026, 6, 15, 15, 29, 30, 0, time.UTC),
		Author:    &discordgo.User{ID: "caller", Username: "caller"},
		Call: &discordgo.MessageCall{
			Participants: append([]string(nil), participants...),
		},
	}
}
