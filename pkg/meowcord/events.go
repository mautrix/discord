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
	"encoding/json"
)

// This file contains all the possible structs that can be
// handled by AddHandler/EventHandler.
// DO NOT ADD ANYTHING BUT EVENT HANDLER STRUCTS TO THIS FILE.
//go:generate go run tools/cmd/eventhandlers/main.go

// Connect is the data for a Connect event.
// This is a synthetic event and is not dispatched by Discord.
type Connect struct{}

// Disconnect is the data for a Disconnect event.
// This is a synthetic event and is not dispatched by Discord.
type Disconnect struct{}

// InvalidAuth is the data for a InvalidAuth event.
// This is a synthetic event and is not dispatched by Discord.
type InvalidAuth struct{}

// RateLimit is the data for a RateLimit event.
// This is a synthetic event and is not dispatched by Discord.
type RateLimit struct {
	*TooManyRequests
	URL string
}

// Event provides a basic initial struct for all websocket events.
type Event struct {
	Operation int             `json:"op"`
	Sequence  int64           `json:"s"`
	Type      string          `json:"t"`
	RawData   json.RawMessage `json:"d"`
	// Struct contains one of the other types in this file.
	Struct interface{} `json:"-"`
}

// A Ready stores all data for the websocket READY event.
type Ready struct {
	Version          int          `json:"v"`
	SessionID        string       `json:"session_id"`
	User             *User        `json:"user"`
	Shard            *[2]int      `json:"shard"`
	Application      *Application `json:"application"`
	PrivateChannels  []*Channel   `json:"private_channels"`
	Guilds           []*Guild     `json:"guilds"`
	ResumeGatewayURL string       `json:"resume_gateway_url"`

	// Undocumented fields
	ReadState         *ReadStateList         `json:"read_state"`
	Settings          *Settings              `json:"user_settings"`
	UserGuildSettings *UserGuildSettingsList `json:"user_guild_settings"`
	Relationships     []*Relationship        `json:"relationships"`
	Presences         []*Presence            `json:"presences"`
	Notes             map[string]string      `json:"notes"`
	MergedMembers     [][]*Member            `json:"merged_members"`
	RequiredAction    RequiredAction         `json:"required_action"`

	Users []*User `json:"users"`
}

type ReadySupplemental struct {
	MergedMembers       [][]*Member     `json:"merged_members"`
	Guilds              []*MinimalGuild `json:"guilds"`
	Disclose            []string        `json:"disclose"`
	LazyPrivateChannels []*Channel      `json:"lazy_private_channels"`
	// Also has merged_presences
}

// ChannelCreate is the data for a ChannelCreate event.
type ChannelCreate struct {
	*Channel
}

// ChannelUpdate is the data for a ChannelUpdate event.
type ChannelUpdate struct {
	*Channel
	BeforeUpdate *Channel `json:"-"`
}

// ChannelDelete is the data for a ChannelDelete event.
type ChannelDelete struct {
	*Channel
	BeforeDelete *Channel `json:"-"`
}

// ChannelPinsUpdate stores data for a ChannelPinsUpdate event.
type ChannelPinsUpdate struct {
	LastPinTimestamp string `json:"last_pin_timestamp"`
	ChannelID        string `json:"channel_id"`
	GuildID          string `json:"guild_id,omitempty"`
}

// ThreadCreate is the data for a ThreadCreate event.
type ThreadCreate struct {
	*Channel
	NewlyCreated bool `json:"newly_created"`
}

// ThreadUpdate is the data for a ThreadUpdate event.
type ThreadUpdate struct {
	*Channel
	BeforeUpdate *Channel `json:"-"`
}

// ThreadDelete is the data for a ThreadDelete event.
type ThreadDelete struct {
	*Channel
	BeforeDelete *Channel `json:"-"`
}

// ThreadListSync is the data for a ThreadListSync event.
type ThreadListSync struct {
	// The id of the guild
	GuildID string `json:"guild_id"`
	// The parent channel ids whose threads are being synced.
	// If omitted, then threads were synced for the entire guild.
	// This array may contain channel_ids that have no active threads as well, so you know to clear that data.
	ChannelIDs []string `json:"channel_ids"`
	// All active threads in the given channels that the current user can access
	Threads []*Channel `json:"threads"`
	// All thread member objects from the synced threads for the current user,
	// indicating which threads the current user has been added to
	Members []*ThreadMember `json:"members"`
}

// ThreadMemberUpdate is the data for a ThreadMemberUpdate event.
type ThreadMemberUpdate struct {
	*ThreadMember
	GuildID string `json:"guild_id"`
}

// ThreadMembersUpdate is the data for a ThreadMembersUpdate event.
type ThreadMembersUpdate struct {
	ID             string              `json:"id"`
	GuildID        string              `json:"guild_id"`
	MemberCount    int                 `json:"member_count"`
	AddedMembers   []AddedThreadMember `json:"added_members"`
	RemovedMembers []string            `json:"removed_member_ids"`
}

// GuildCreate is the data for a GuildCreate event.
type GuildCreate struct {
	*Guild
}

// GuildUpdate is the data for a GuildUpdate event.
type GuildUpdate struct {
	*Guild
}

// GuildDelete is the data for a GuildDelete event.
type GuildDelete struct {
	*Guild
	BeforeDelete *Guild `json:"-"`
}

// GuildBanAdd is the data for a GuildBanAdd event.
type GuildBanAdd struct {
	User    *User  `json:"user"`
	GuildID string `json:"guild_id"`
}

// GuildBanRemove is the data for a GuildBanRemove event.
type GuildBanRemove struct {
	User    *User  `json:"user"`
	GuildID string `json:"guild_id"`
}

// GuildMemberAdd is the data for a GuildMemberAdd event.
type GuildMemberAdd struct {
	*Member
}

// GuildMemberUpdate is the data for a GuildMemberUpdate event.
type GuildMemberUpdate struct {
	*Member
	BeforeUpdate *Member `json:"-"`
}

// GuildMemberRemove is the data for a GuildMemberRemove event.
type GuildMemberRemove struct {
	*Member
	BeforeDelete *Member `json:"-"`
}

// GuildRoleCreate is the data for a GuildRoleCreate event.
type GuildRoleCreate struct {
	*GuildRole
}

// GuildRoleUpdate is the data for a GuildRoleUpdate event.
type GuildRoleUpdate struct {
	*GuildRole
	BeforeUpdate *Role `json:"-"`
}

// A GuildRoleDelete is the data for a GuildRoleDelete event.
type GuildRoleDelete struct {
	RoleID       string `json:"role_id"`
	GuildID      string `json:"guild_id"`
	BeforeDelete *Role  `json:"-"`
}

// A GuildEmojisUpdate is the data for a guild emoji update event.
type GuildEmojisUpdate struct {
	GuildID string   `json:"guild_id"`
	Emojis  []*Emoji `json:"emojis"`
}

// A GuildStickersUpdate is the data for a GuildStickersUpdate event.
type GuildStickersUpdate struct {
	GuildID  string     `json:"guild_id"`
	Stickers []*Sticker `json:"stickers"`
}

// A GuildMembersChunk is the data for a GuildMembersChunk event.
type GuildMembersChunk struct {
	GuildID    string      `json:"guild_id"`
	Members    []*Member   `json:"members"`
	ChunkIndex int         `json:"chunk_index"`
	ChunkCount int         `json:"chunk_count"`
	NotFound   []string    `json:"not_found,omitempty"`
	Presences  []*Presence `json:"presences,omitempty"`
	Nonce      string      `json:"nonce,omitempty"`
}

// GuildIntegrationsUpdate is the data for a GuildIntegrationsUpdate event.
type GuildIntegrationsUpdate struct {
	GuildID string `json:"guild_id"`
}

// StageInstanceEventCreate is the data for a StageInstanceEventCreate event.
type StageInstanceEventCreate struct {
	*StageInstance
}

// StageInstanceEventUpdate is the data for a StageInstanceEventUpdate event.
type StageInstanceEventUpdate struct {
	*StageInstance
}

// StageInstanceEventDelete is the data for a StageInstanceEventDelete event.
type StageInstanceEventDelete struct {
	*StageInstance
}

// GuildScheduledEventCreate is the data for a GuildScheduledEventCreate event.
type GuildScheduledEventCreate struct {
	*GuildScheduledEvent
}

// GuildScheduledEventUpdate is the data for a GuildScheduledEventUpdate event.
type GuildScheduledEventUpdate struct {
	*GuildScheduledEvent
}

// GuildScheduledEventDelete is the data for a GuildScheduledEventDelete event.
type GuildScheduledEventDelete struct {
	*GuildScheduledEvent
}

// GuildScheduledEventUserAdd is the data for a GuildScheduledEventUserAdd event.
type GuildScheduledEventUserAdd struct {
	GuildScheduledEventID string `json:"guild_scheduled_event_id"`
	UserID                string `json:"user_id"`
	GuildID               string `json:"guild_id"`
}

// GuildScheduledEventUserRemove is the data for a GuildScheduledEventUserRemove event.
type GuildScheduledEventUserRemove struct {
	GuildScheduledEventID string `json:"guild_scheduled_event_id"`
	UserID                string `json:"user_id"`
	GuildID               string `json:"guild_id"`
}

// MessageAck is the data for a MessageAck event.
type MessageAck struct {
	Version   int    `json:"version"`
	MessageID string `json:"message_id"`
	ChannelID string `json:"channel_id"`
}

// IntegrationCreate is the data for a IntegrationCreate event.
type IntegrationCreate struct {
	*Integration
	GuildID string `json:"guild_id"`
}

// IntegrationUpdate is the data for a IntegrationUpdate event.
type IntegrationUpdate struct {
	*Integration
	GuildID string `json:"guild_id"`
}

// IntegrationDelete is the data for a IntegrationDelete event.
type IntegrationDelete struct {
	ID            string `json:"id"`
	GuildID       string `json:"guild_id"`
	ApplicationID string `json:"application_id,omitempty"`
}

// MessageCreate is the data for a MessageCreate event.
type MessageCreate struct {
	*Message
}

// UnmarshalJSON is a helper function to unmarshal MessageCreate object.
func (m *MessageCreate) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &m.Message)
}

// MessageUpdate is the data for a MessageUpdate event.
type MessageUpdate struct {
	*Message
	// BeforeUpdate will be nil if the Message was not previously cached in the state cache.
	BeforeUpdate *Message `json:"-"`
}

// UnmarshalJSON is a helper function to unmarshal MessageUpdate object.
func (m *MessageUpdate) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &m.Message)
}

// MessageDelete is the data for a MessageDelete event.
type MessageDelete struct {
	*Message
	BeforeDelete *Message `json:"-"`
}

// UnmarshalJSON is a helper function to unmarshal MessageDelete object.
func (m *MessageDelete) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &m.Message)
}

// MessageReactionAdd is the data for a MessageReactionAdd event.
type MessageReactionAdd struct {
	*MessageReaction
	Member *Member `json:"member,omitempty"`
}

// MessageReactionRemove is the data for a MessageReactionRemove event.
type MessageReactionRemove struct {
	*MessageReaction
}

// MessageReactionRemoveAll is the data for a MessageReactionRemoveAll event.
type MessageReactionRemoveAll struct {
	*MessageReaction
}

// MessageReactionRemoveEmoji is the data for a MessageReactionRemoveEmoji event.
type MessageReactionRemoveEmoji struct {
	*MessageReaction
}

// PresencesReplace is the data for a PresencesReplace event.
type PresencesReplace []*Presence

// PresenceUpdate is the data for a PresenceUpdate event.
type PresenceUpdate struct {
	Presence
	GuildID string `json:"guild_id"`
}

// Resumed is the data for a Resumed event.
type Resumed struct {
	Trace []string `json:"_trace"`
}

// RelationshipAdd is the data for a RelationshipAdd event.
type RelationshipAdd struct {
	*Relationship
}

// RelationshipRemove is the data for a RelationshipRemove event.
type RelationshipRemove struct {
	*Relationship
}

// RelationshipUpdate is the data for a RelationshipUpdate event.
type RelationshipUpdate struct {
	*Relationship
}

type ChannelRecipientRemove struct {
	ChannelID string `json:"channel_id"`
	User      *User  `json:"user"`
}

type ChannelRecipientAdd struct {
	ChannelID string `json:"channel_id"`
	User      *User  `json:"user"`
}

// TypingStart is the data for a TypingStart event.
type TypingStart struct {
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id,omitempty"`
	Timestamp int    `json:"timestamp"`
}

// UserUpdate is the data for a UserUpdate event.
type UserUpdate struct {
	*User
}

// UserSettingsUpdate is the data for a UserSettingsUpdate event.
type UserSettingsUpdate map[string]interface{}

// UserGuildSettingsUpdate is the data for a UserGuildSettingsUpdate event.
type UserGuildSettingsUpdate struct {
	*UserGuildSettings
}

// UserNoteUpdate is the data for a UserNoteUpdate event.
type UserNoteUpdate struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

// VoiceServerUpdate is the data for a VoiceServerUpdate event.
type VoiceServerUpdate struct {
	Token    string `json:"token"`
	GuildID  string `json:"guild_id"`
	Endpoint string `json:"endpoint"`
}

// VoiceStateUpdate is the data for a VoiceStateUpdate event.
type VoiceStateUpdate struct {
	*VoiceState
	// BeforeUpdate will be nil if the VoiceState was not previously cached in the state cache.
	BeforeUpdate *VoiceState `json:"-"`
}

// MessageDeleteBulk is the data for a MessageDeleteBulk event
type MessageDeleteBulk struct {
	Messages  []string `json:"ids"`
	ChannelID string   `json:"channel_id"`
	GuildID   string   `json:"guild_id"`
}

// WebhooksUpdate is the data for a WebhooksUpdate event
type WebhooksUpdate struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

// InteractionCreate is the data for a InteractionCreate event
type InteractionCreate struct {
	*Interaction
}

// UnmarshalJSON is a helper function to unmarshal Interaction object.
func (i *InteractionCreate) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &i.Interaction)
}

type InteractionSuccess struct {
	Nonce string `json:"nonce"`
	ID    string `json:"id"`
}

// InviteCreate is the data for a InviteCreate event
type InviteCreate struct {
	*Invite
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
}

// InviteDelete is the data for a InviteDelete event
type InviteDelete struct {
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Code      string `json:"code"`
}

// ApplicationCommandPermissionsUpdate is the data for an ApplicationCommandPermissionsUpdate event
type ApplicationCommandPermissionsUpdate struct {
	*GuildApplicationCommandPermissions
}

// AutoModerationRuleCreate is the data for an AutoModerationRuleCreate event.
type AutoModerationRuleCreate struct {
	*AutoModerationRule
}

// AutoModerationRuleUpdate is the data for an AutoModerationRuleUpdate event.
type AutoModerationRuleUpdate struct {
	*AutoModerationRule
}

// AutoModerationRuleDelete is the data for an AutoModerationRuleDelete event.
type AutoModerationRuleDelete struct {
	*AutoModerationRule
}

// AutoModerationActionExecution is the data for an AutoModerationActionExecution event.
type AutoModerationActionExecution struct {
	GuildID              string                        `json:"guild_id"`
	Action               AutoModerationAction          `json:"action"`
	RuleID               string                        `json:"rule_id"`
	RuleTriggerType      AutoModerationRuleTriggerType `json:"rule_trigger_type"`
	UserID               string                        `json:"user_id"`
	ChannelID            string                        `json:"channel_id"`
	MessageID            string                        `json:"message_id"`
	AlertSystemMessageID string                        `json:"alert_system_message_id"`
	Content              string                        `json:"content"`
	MatchedKeyword       string                        `json:"matched_keyword"`
	MatchedContent       string                        `json:"matched_content"`
}

// GuildAuditLogEntryCreate is the data for a GuildAuditLogEntryCreate event.
type GuildAuditLogEntryCreate struct {
	*AuditLogEntry
	GuildID string `json:"guild_id"`
}

// MessagePollVoteAdd is the data for a MessagePollVoteAdd event.
type MessagePollVoteAdd struct {
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	GuildID   string `json:"guild_id,omitempty"`
	AnswerID  int    `json:"answer_id"`
}

// MessagePollVoteRemove is the data for a MessagePollVoteRemove event.
type MessagePollVoteRemove struct {
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	GuildID   string `json:"guild_id,omitempty"`
	AnswerID  int    `json:"answer_id"`
}

// EntitlementCreate is the data for an EntitlementCreate event.
type EntitlementCreate struct {
	*Entitlement
}

// EntitlementUpdate is the data for an EntitlementUpdate event.
type EntitlementUpdate struct {
	*Entitlement
}

// EntitlementDelete is the data for an EntitlementDelete event.
// NOTE: Entitlements are not deleted when they expire.
type EntitlementDelete struct {
	*Entitlement
}

// SubscriptionCreate is the data for an SubscriptionCreate event.
// https://discord.com/developers/docs/monetization/implementing-app-subscriptions#using-subscription-events-for-the-subscription-lifecycle
type SubscriptionCreate struct {
	*Subscription
}

// SubscriptionUpdate is the data for an SubscriptionUpdate event.
// https://discord.com/developers/docs/monetization/implementing-app-subscriptions#using-subscription-events-for-the-subscription-lifecycle
type SubscriptionUpdate struct {
	*Subscription
}

// SubscriptionDelete is the data for an SubscriptionDelete event.
// https://discord.com/developers/docs/monetization/implementing-app-subscriptions#using-subscription-events-for-the-subscription-lifecycle
type SubscriptionDelete struct {
	*Subscription
}

// UserRequiredActionUpdate contains an updated [RequiredAction] for the user
// account.
type UserRequiredActionUpdate struct {
	RequiredAction RequiredAction `json:"required_action"`
}
