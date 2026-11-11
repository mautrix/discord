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
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
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
	EditedTimestamp time.Time      `json:"edited_timestamp,omitzero"`
	ContentHash     []byte         `json:"content_hash,omitempty"`
	Media           *MediaMetadata `json:"media,omitempty"`
	Text            *MediaCaption  `json:"text,omitempty"`
	NonMedia        bool           `json:"non_media,omitempty"`
}

type MediaCaption struct {
	Body          string       `json:"body"`
	Format        event.Format `json:"format,omitempty"`
	FormattedBody string       `json:"formatted_body,omitempty"`
	ExtraHash     string       `json:"extra_hash,omitempty"`
}

func NewMediaCaption(content *event.MessageEventContent, extra ...map[string]any) *MediaCaption {
	caption := &MediaCaption{Body: content.Body, Format: content.Format, FormattedBody: content.FormattedBody}
	if caption.Format == event.FormatHTML && caption.FormattedBody == event.TextToHTML(caption.Body) {
		caption.Format = ""
		caption.FormattedBody = ""
	}
	if len(extra) > 0 {
		data, err := json.Marshal(extra[0]["com.beeper.linkpreviews"])
		var value any
		if err == nil {
			err = json.Unmarshal(data, &value)
		}
		if err == nil {
			data, err = json.Marshal(value)
		}
		if err == nil && string(data) != "null" && string(data) != "[]" {
			hash := sha256.Sum256(data)
			caption.ExtraHash = hex.EncodeToString(hash[:])
		}
	}
	return caption
}

type MediaMetadata struct {
	SourceID    string                     `json:"source_id"`
	Filename    string                     `json:"filename"`
	Description string                     `json:"description,omitempty"`
	Spoiler     bool                       `json:"spoiler,omitempty"`
	Caption     *MediaCaption              `json:"caption,omitempty"`
	Content     *event.MessageEventContent `json:"content"`
	Legacy      bool                       `json:"-"`
	LegacyExtra map[string]any             `json:"-"`
	Unavailable bool                       `json:"-"`
}

func (meta *MessageMetadata) CopyFrom(incoming any) {
	other, ok := incoming.(*MessageMetadata)
	if !ok || other == nil {
		return
	}
	meta.EditedTimestamp = other.EditedTimestamp
	meta.ContentHash = other.ContentHash
	if other.Media != nil || other.Text == nil {
		meta.Media = other.Media
		meta.Text = nil
		meta.NonMedia = other.NonMedia
	}
	if other.Text != nil {
		if meta.Media != nil {
			meta.Media.Caption = other.Text
		} else {
			meta.Text = other.Text
			meta.NonMedia = true
		}
	}
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
