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

package discordid

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/database"
)

type PortalMetadata struct {
	// The ID of the Discord guild that the channel corresponding to this portal
	// belongs to.
	//
	// For private channels (DMs and group DMs), this will be the zero value
	// (an empty string).
	GuildID string `json:"guild_id"`

	// The type of Discord channel this portal corresponds to.
	//
	// This is omitted for guild space portals.
	ChannelType *discordgo.ChannelType `json:"channel_type,omitempty"`
}

type UserLoginMetadata struct {
	Token            string                     `json:"token"`
	HeartbeatSession discordgo.HeartbeatSession `json:"heartbeat_session"`
	BridgedGuildIDs  map[string]bool            `json:"bridged_guild_ids,omitempty"`
}

var _ database.MetaMerger = (*UserLoginMetadata)(nil)

func (ulm *UserLoginMetadata) CopyFrom(incoming any) {
	incomingMeta, ok := incoming.(*UserLoginMetadata)
	if !ok || incomingMeta == nil {
		return
	}

	if incomingMeta.Token != "" {
		ulm.Token = incomingMeta.Token
	}
	ulm.HeartbeatSession = discordgo.NewHeartbeatSession()

	// Retain the BridgedGuildIDs from the existing login.
}

type MessageMetadata struct {
	EditedTimestamp time.Time `json:"edited_timestamp,omitzero"`
	ContentHash     []byte    `json:"content_hash,omitempty"`
}

func NewMessageMetadata(msg *discordgo.Message) *MessageMetadata {
	meta := &MessageMetadata{}
	if msg.EditedTimestamp != nil {
		meta.EditedTimestamp = msg.EditedTimestamp.Truncate(time.Millisecond)
	}
	attachmentIDs := make([]string, len(msg.Attachments))
	for i, att := range msg.Attachments {
		attachmentIDs[i] = att.ID + "/" + att.Filename + "/" + att.Description
	}
	stickerIDs := make([]string, len(msg.StickerItems))
	for i, sticker := range msg.StickerItems {
		stickerIDs[i] = sticker.ID
	}
	data, err := json.Marshal(map[string]any{
		"content":           msg.Content,
		"embeds":            msg.Embeds,
		"attachments":       attachmentIDs,
		"stickers":          stickerIDs,
		"components":        msg.Components,
		"message_snapshots": msg.MessageSnapshots,
		"poll":              msg.Poll,
		"mention_everyone":  msg.MentionEveryone,
	})
	if err == nil {
		hash := sha256.Sum256(data)
		meta.ContentHash = hash[:]
	}
	return meta
}
