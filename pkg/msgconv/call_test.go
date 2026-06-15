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
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestRenderDiscordCallMessageActive(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111"},
		},
	}
	want := "(caller started a call. Use the Discord app to answer.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithParticipantsInDM(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111", "222"},
		},
	}
	want := "(caller started a call. Use the Discord app to answer.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithCurrentUserInDMCall(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			ID:            "111",
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111", "222"},
		},
	}
	options := discordCallRenderOptions{CurrentUserID: "222"}
	want := "(You are in a call with caller. Use the Discord app to manage the call.)"
	if got := renderDiscordCallMessage(msg, options); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithoutCurrentUserInDMCall(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			ID:            "111",
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111"},
		},
	}
	options := discordCallRenderOptions{CurrentUserID: "222"}
	want := "(caller started a call. Use the Discord app to answer.)"
	if got := renderDiscordCallMessage(msg, options); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithParticipantsInGroupDM(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			ID:            "111",
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111", "222"},
		},
	}
	options := discordCallRenderOptions{ShowActiveParticipants: true, CurrentUserID: "333"}
	want := "(caller started a call. 2 people are in the call. Use the Discord app to join.)"
	if got := renderDiscordCallMessage(msg, options); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithCurrentUserInGroupDM(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			ID:            "111",
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111", "222"},
		},
	}
	options := discordCallRenderOptions{ShowActiveParticipants: true, CurrentUserID: "222"}
	want := "(caller started a call. 2 people are in the call. Use the Discord app to manage the call.)"
	if got := renderDiscordCallMessage(msg, options); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageActiveWithOneParticipantInGroupDM(t *testing.T) {
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			ID:            "111",
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111"},
		},
	}
	options := discordCallRenderOptions{ShowActiveParticipants: true, CurrentUserID: "222"}
	want := "(caller started a call. 1 person is in the call. Use the Discord app to join.)"
	if got := renderDiscordCallMessage(msg, options); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageEnded(t *testing.T) {
	started := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	ended := started.Add(2*time.Minute + 3*time.Second)
	msg := &discordgo.Message{
		Type:      discordgo.MessageTypeCall,
		Timestamp: started,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants:   []string{"111", "222"},
			EndedTimestamp: &ended,
		},
	}
	want := "(caller started a call that lasted 2m 3s.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageEndedImmediately(t *testing.T) {
	started := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	msg := &discordgo.Message{
		Type:      discordgo.MessageTypeCall,
		Timestamp: started,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants:   []string{"111"},
			EndedTimestamp: &started,
		},
	}
	want := "(caller started a call that lasted a few seconds.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageEndedBeforeStartTime(t *testing.T) {
	started := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	ended := started.Add(-time.Second)
	msg := &discordgo.Message{
		Type:      discordgo.MessageTypeCall,
		Timestamp: started,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants:   []string{"111"},
			EndedTimestamp: &ended,
		},
	}
	want := "(caller started a call that ended.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestRenderDiscordCallMessageEndedWithoutStartTime(t *testing.T) {
	ended := time.Date(2026, 6, 15, 10, 2, 3, 0, time.UTC)
	msg := &discordgo.Message{
		Type: discordgo.MessageTypeCall,
		Author: &discordgo.User{
			Username:      "caller",
			Discriminator: "0",
		},
		Call: &discordgo.MessageCall{
			Participants:   []string{"111"},
			EndedTimestamp: &ended,
		},
	}
	want := "(caller started a call that ended.)"
	if got := renderDiscordCallMessage(msg, discordCallRenderOptions{}); got != want {
		t.Fatalf("renderDiscordCallMessage() = %q, want %q", got, want)
	}
}

func TestFormatDiscordCallDuration(t *testing.T) {
	tests := map[string]struct {
		duration time.Duration
		want     string
	}{
		"zero":             {duration: 0, want: "a few seconds"},
		"very short":       {duration: 4*time.Second + 999*time.Millisecond, want: "a few seconds"},
		"short threshold":  {duration: 5 * time.Second, want: "5s"},
		"rounds down":      {duration: 9*time.Second + 400*time.Millisecond, want: "9s"},
		"rounds up":        {duration: 8*time.Second + 600*time.Millisecond, want: "9s"},
		"exact minute":     {duration: time.Minute, want: "1m"},
		"minute seconds":   {duration: 2*time.Minute + 3*time.Second, want: "2m 3s"},
		"exact hour":       {duration: time.Hour, want: "1h"},
		"hour minute":      {duration: time.Hour + 2*time.Minute, want: "1h 2m"},
		"hour minute sec":  {duration: time.Hour + 2*time.Minute + 3*time.Second, want: "1h 2m 3s"},
		"minute carry":     {duration: 59*time.Second + 600*time.Millisecond, want: "1m"},
		"hour carry":       {duration: 59*time.Minute + 59*time.Second + 600*time.Millisecond, want: "1h"},
		"negative guarded": {duration: -time.Second, want: "a few seconds"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := formatDiscordCallDuration(test.duration); got != test.want {
				t.Fatalf("formatDiscordCallDuration(%s) = %q, want %q", test.duration, got, test.want)
			}
		})
	}
}
