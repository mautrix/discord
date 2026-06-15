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
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestFillMissingMessageUpdateFieldsPreservesCallContext(t *testing.T) {
	started := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	ended := started.Add(2*time.Minute + 3*time.Second)
	before := &discordgo.Message{
		ID:        "123",
		ChannelID: "456",
		Type:      discordgo.MessageTypeCall,
		Timestamp: started,
		Author: &discordgo.User{
			ID:       "111",
			Username: "caller",
		},
		Call: &discordgo.MessageCall{
			Participants: []string{"111"},
		},
	}
	update := &discordgo.Message{
		ID:        "123",
		ChannelID: "456",
		Call: &discordgo.MessageCall{
			Participants:   []string{"111", "222"},
			EndedTimestamp: &ended,
		},
	}

	fillMissingMessageUpdateFields(update, before)

	if update.Author == nil || update.Author.ID != "111" {
		t.Fatalf("Author = %#v, want caller from previous message", update.Author)
	}
	if update.Type != discordgo.MessageTypeCall {
		t.Fatalf("Type = %d, want %d", update.Type, discordgo.MessageTypeCall)
	}
	if !update.Timestamp.Equal(started) {
		t.Fatalf("Timestamp = %s, want %s", update.Timestamp.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano))
	}
	if update.Call == nil {
		t.Fatal("Call is nil")
	}
	if update.Call.EndedTimestamp == nil || !update.Call.EndedTimestamp.Equal(ended) {
		t.Fatalf("Call.EndedTimestamp = %#v, want %s", update.Call.EndedTimestamp, ended.Format(time.RFC3339Nano))
	}
}
