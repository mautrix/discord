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
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type discordCallRenderOptions struct {
	ShowActiveParticipants bool
	CurrentUserID          string
}

func renderDiscordCallMessage(msg *discordgo.Message, options discordCallRenderOptions) string {
	caller := "Someone"
	if msg.Author != nil {
		caller = msg.Author.String()
	}

	if msg.Call == nil || msg.Call.EndedTimestamp == nil {
		if msg.Call != nil {
			currentUserInCall := isDiscordCallParticipant(msg.Call, options.CurrentUserID)
			if options.ShowActiveParticipants && len(msg.Call.Participants) > 0 {
				action := "Use the Discord app to join."
				if currentUserInCall {
					action = "Use the Discord app to manage the call."
				}
				return fmt.Sprintf("(%s started a call. %s. %s)", caller, formatDiscordCallParticipantCount(len(msg.Call.Participants)), action)
			}
			if currentUserInCall {
				if msg.Author != nil && msg.Author.ID != options.CurrentUserID {
					return fmt.Sprintf("(You are in a call with %s. Use the Discord app to manage the call.)", caller)
				}
				return "(You are in a call. Use the Discord app to manage the call.)"
			}
		}
		return fmt.Sprintf("(%s started a call. Use the Discord app to answer.)", caller)
	}

	if msg.Timestamp.IsZero() || msg.Call.EndedTimestamp.Before(msg.Timestamp) {
		return fmt.Sprintf("(%s started a call that ended.)", caller)
	}
	return fmt.Sprintf("(%s started a call that lasted %s.)", caller, formatDiscordCallDuration(msg.Call.EndedTimestamp.Sub(msg.Timestamp)))
}

func isDiscordCallParticipant(call *discordgo.MessageCall, userID string) bool {
	if userID == "" {
		return false
	}
	for _, participantID := range call.Participants {
		if participantID == userID {
			return true
		}
	}
	return false
}

func formatDiscordCallParticipantCount(count int) string {
	if count == 1 {
		return "1 person is in the call"
	}
	return fmt.Sprintf("%d people are in the call", count)
}

func formatDiscordCallDuration(duration time.Duration) string {
	if duration < 5*time.Second {
		return "a few seconds"
	}

	duration = duration.Round(time.Second)

	hours := int(duration / time.Hour)
	duration -= time.Duration(hours) * time.Hour
	minutes := int(duration / time.Minute)
	duration -= time.Duration(minutes) * time.Minute
	seconds := int(duration / time.Second)

	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}
