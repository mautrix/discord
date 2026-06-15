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
	"context"
	"slices"
	"sync"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
)

type discordCallState struct {
	lock               sync.Mutex
	activeByChannel    map[string]*discordgo.Message
	voiceChannelByUser map[string]string
}

func newDiscordCallState() *discordCallState {
	return &discordCallState{
		activeByChannel:    make(map[string]*discordgo.Message),
		voiceChannelByUser: make(map[string]string),
	}
}

func (s *discordCallState) UpdateFromMessage(msg *discordgo.Message) {
	if s == nil || msg == nil || msg.Type != discordgo.MessageTypeCall || msg.ChannelID == "" {
		return
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	if msg.Call == nil || msg.Call.EndedTimestamp != nil {
		s.removeChannelLocked(msg.ChannelID)
		return
	}

	s.removeStaleParticipantMappingsLocked(msg.ChannelID, msg.Call.Participants)
	active := cloneDiscordCallMessage(msg)
	s.activeByChannel[msg.ChannelID] = active
	for _, userID := range active.Call.Participants {
		s.voiceChannelByUser[userID] = msg.ChannelID
	}
}

func (s *discordCallState) UpdateFromVoiceState(evt *discordgo.VoiceStateUpdate) []*discordgo.Message {
	if s == nil || evt == nil || evt.VoiceState == nil || evt.UserID == "" {
		return nil
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	var edits []*discordgo.Message
	oldChannelID := s.voiceChannelByUser[evt.UserID]
	if oldChannelID == "" && evt.BeforeUpdate != nil {
		oldChannelID = evt.BeforeUpdate.ChannelID
	}
	newChannelID := evt.ChannelID

	if oldChannelID != "" && oldChannelID != newChannelID {
		if edit := s.removeParticipantLocked(oldChannelID, evt.UserID); edit != nil {
			edits = append(edits, edit)
		}
	}

	if newChannelID == "" {
		delete(s.voiceChannelByUser, evt.UserID)
		return edits
	}

	s.voiceChannelByUser[evt.UserID] = newChannelID
	if edit := s.addParticipantLocked(newChannelID, evt.UserID); edit != nil {
		edits = append(edits, edit)
	}
	return edits
}

func (s *discordCallState) removeChannelLocked(channelID string) {
	if active := s.activeByChannel[channelID]; active != nil && active.Call != nil {
		for _, userID := range active.Call.Participants {
			if s.voiceChannelByUser[userID] == channelID {
				delete(s.voiceChannelByUser, userID)
			}
		}
	}
	delete(s.activeByChannel, channelID)
}

func (s *discordCallState) removeStaleParticipantMappingsLocked(channelID string, participants []string) {
	active := s.activeByChannel[channelID]
	if active == nil || active.Call == nil {
		return
	}
	for _, userID := range active.Call.Participants {
		if !slices.Contains(participants, userID) && s.voiceChannelByUser[userID] == channelID {
			delete(s.voiceChannelByUser, userID)
		}
	}
}

func (s *discordCallState) removeParticipantLocked(channelID, userID string) *discordgo.Message {
	active := s.activeByChannel[channelID]
	if active == nil || active.Call == nil {
		return nil
	}

	participantIdx := slices.Index(active.Call.Participants, userID)
	if participantIdx == -1 {
		return nil
	}
	active.Call.Participants = slices.Delete(active.Call.Participants, participantIdx, participantIdx+1)
	return cloneDiscordCallMessage(active)
}

func (s *discordCallState) addParticipantLocked(channelID, userID string) *discordgo.Message {
	active := s.activeByChannel[channelID]
	if active == nil || active.Call == nil || slices.Contains(active.Call.Participants, userID) {
		return nil
	}

	active.Call.Participants = append(active.Call.Participants, userID)
	return cloneDiscordCallMessage(active)
}

func cloneDiscordCallMessage(msg *discordgo.Message) *discordgo.Message {
	if msg == nil {
		return nil
	}

	clone := *msg
	if msg.Author != nil {
		author := *msg.Author
		clone.Author = &author
	}
	if msg.Call != nil {
		clone.Call = &discordgo.MessageCall{
			Participants: append([]string(nil), msg.Call.Participants...),
		}
		if msg.Call.EndedTimestamp != nil {
			endedTimestamp := *msg.Call.EndedTimestamp
			clone.Call.EndedTimestamp = &endedTimestamp
		}
	}
	return &clone
}

func (d *DiscordClient) trackDiscordCallMessage(msg *discordgo.Message) {
	if d.callState != nil {
		d.callState.UpdateFromMessage(msg)
	}
}

func (d *DiscordClient) handleDiscordCallVoiceStateUpdate(ctx context.Context, evt *discordgo.VoiceStateUpdate) {
	if d.callState == nil {
		return
	}

	for _, msg := range d.callState.UpdateFromVoiceState(evt) {
		d.queueDiscordCallStateEdit(ctx, msg)
	}
}

func (d *DiscordClient) queueDiscordCallStateEdit(ctx context.Context, msg *discordgo.Message) {
	if msg == nil || msg.ID == "" || msg.ChannelID == "" {
		return
	}

	ctx, log := messageCtx(ctx, msg)
	bridged, route := d.channelIsBridged(ctx, msg.ChannelID)
	if !bridged {
		return
	}

	participantCount := 0
	if msg.Call != nil {
		participantCount = len(msg.Call.Participants)
	}
	log.Debug().
		Int("call_participant_count", participantCount).
		Msg("Queueing Discord call state edit from voice state")
	wrappedEvt := d.wrapDiscordMessage(ctx, msg, route, bridgev2.RemoteEventEdit)
	d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
}
