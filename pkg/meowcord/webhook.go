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

// Webhook stores the data for a webhook.
type Webhook struct {
	ID        string      `json:"id"`
	Type      WebhookType `json:"type"`
	GuildID   string      `json:"guild_id"`
	ChannelID string      `json:"channel_id"`
	User      *User       `json:"user"`
	Name      string      `json:"name"`
	Avatar    string      `json:"avatar"`
	Token     string      `json:"token"`

	// ApplicationID is the bot/OAuth2 application that created this webhook
	ApplicationID string `json:"application_id,omitempty"`
}

// WebhookType is the type of Webhook (see WebhookType* consts) in the Webhook struct
// https://discord.com/developers/docs/resources/webhook#webhook-object-webhook-types
type WebhookType int

// Valid WebhookType values
const (
	WebhookTypeIncoming        WebhookType = 1
	WebhookTypeChannelFollower WebhookType = 2
)

// WebhookParams is a struct for webhook params, used in the WebhookExecute command.
type WebhookParams struct {
	Content         string                  `json:"content,omitempty"`
	Username        string                  `json:"username,omitempty"`
	AvatarURL       string                  `json:"avatar_url,omitempty"`
	TTS             bool                    `json:"tts,omitempty"`
	Files           []*File                 `json:"-"`
	Components      []MessageComponent      `json:"components"`
	Embeds          []*MessageEmbed         `json:"embeds,omitempty"`
	Attachments     []*MessageAttachment    `json:"attachments,omitempty"`
	AllowedMentions *MessageAllowedMentions `json:"allowed_mentions,omitempty"`
	// Only MessageFlagsSuppressEmbeds and MessageFlagsEphemeral can be set.
	// MessageFlagsEphemeral can only be set when using Followup Message Create endpoint.
	Flags MessageFlags `json:"flags,omitempty"`
	// Name of the thread to create.
	// NOTE: can only be set if the webhook channel is a forum.
	ThreadName string `json:"thread_name,omitempty"`
}

// WebhookEdit stores data for editing of a webhook message.
type WebhookEdit struct {
	Content         *string                 `json:"content,omitempty"`
	Components      *[]MessageComponent     `json:"components,omitempty"`
	Embeds          *[]*MessageEmbed        `json:"embeds,omitempty"`
	Files           []*File                 `json:"-"`
	Attachments     *[]*MessageAttachment   `json:"attachments,omitempty"`
	AllowedMentions *MessageAllowedMentions `json:"allowed_mentions,omitempty"`
	Flags           MessageFlags            `json:"flags,omitempty"`
}
