// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

package config

import (
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/id"
)

type BridgeConfig struct {
	UsernameTemplate          string `yaml:"username_template"`
	DisplaynameTemplate       string `yaml:"displayname_template"`
	ChannelNameTemplate       string `yaml:"channel_name_template"`
	GuildNameTemplate         string `yaml:"guild_name_template"`
	PrivateChatPortalMeta     string `yaml:"private_chat_portal_meta"`
	PrivateChannelCreateLimit int    `yaml:"startup_private_channel_create_limit"`

	PortalMessageBuffer int `yaml:"portal_message_buffer"`

	DeliveryReceipts            bool `yaml:"delivery_receipts"`
	MessageStatusEvents         bool `yaml:"message_status_events"`
	MessageErrorNotices         bool `yaml:"message_error_notices"`
	RestrictedRooms             bool `yaml:"restricted_rooms"`
	AutojoinThreadOnOpen        bool `yaml:"autojoin_thread_on_open"`
	EmbedFieldsAsTables         bool `yaml:"embed_fields_as_tables"`
	MuteChannelsOnCreate        bool `yaml:"mute_channels_on_create"`
	SyncDirectChatList          bool `yaml:"sync_direct_chat_list"`
	ResendBridgeInfo            bool `yaml:"resend_bridge_info"`
	CustomEmojiReactions        bool `yaml:"custom_emoji_reactions"`
	DeletePortalOnChannelDelete bool `yaml:"delete_portal_on_channel_delete"`
	DeleteGuildOnLeave          bool `yaml:"delete_guild_on_leave"`
	FederateRooms               bool `yaml:"federate_rooms"`
	PrefixWebhookMessages       bool `yaml:"prefix_webhook_messages"`
	EnableWebhookAvatars        bool `yaml:"enable_webhook_avatars"`
	UseDiscordCDNUpload         bool `yaml:"use_discord_cdn_upload"`

	CacheMedia    string        `yaml:"cache_media"`
	MediaPatterns MediaPatterns `yaml:"media_patterns"`

	AnimatedSticker struct {
		Target string `yaml:"target"`
		Args   struct {
			Width  int `yaml:"width"`
			Height int `yaml:"height"`
			FPS    int `yaml:"fps"`
		} `yaml:"args"`
	} `yaml:"animated_sticker"`

	DoublePuppetServerMap      map[string]string `yaml:"double_puppet_server_map"`
	DoublePuppetAllowDiscovery bool              `yaml:"double_puppet_allow_discovery"`
	LoginSharedSecretMap       map[string]string `yaml:"login_shared_secret_map"`

	CommandPrefix      string                           `yaml:"command_prefix"`
	ManagementRoomText bridgeconfig.ManagementRoomTexts `yaml:"management_room_text"`

	Backfill struct {
		Limits struct {
			Initial BackfillLimitPart `yaml:"initial"`
			Missed  BackfillLimitPart `yaml:"missed"`
		} `yaml:"forward_limits"`
		MaxGuildMembers int `yaml:"max_guild_members"`
	} `yaml:"backfill"`

	Encryption bridgeconfig.EncryptionConfig `yaml:"encryption"`

	Provisioning struct {
		Prefix       string `yaml:"prefix"`
		SharedSecret string `yaml:"shared_secret"`
	} `yaml:"provisioning"`

	Permissions bridgeconfig.PermissionConfig `yaml:"permissions"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
	channelNameTemplate *template.Template `yaml:"-"`
	guildNameTemplate   *template.Template `yaml:"-"`
}

type MediaPatterns struct {
	Enabled        bool   `yaml:"enabled"`
	TplAttachments string `yaml:"attachments"`
	TplEmojis      string `yaml:"emojis"`
	TplStickers    string `yaml:"stickers"`
	TplAvatars     string `yaml:"avatars"`

	attachments *template.Template `yaml:"-"`
	emojis      *template.Template `yaml:"-"`
	stickers    *template.Template `yaml:"-"`
	avatars     *template.Template `yaml:"-"`
}

type umMediaPatterns MediaPatterns

func (mp *MediaPatterns) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umMediaPatterns)(mp))
	if err != nil {
		return err
	}
	tpl := template.New("media_patterns")

	pairs := []struct {
		ptr      **template.Template
		name     string
		template string
	}{
		{&mp.attachments, "attachments", mp.TplAttachments},
		{&mp.emojis, "emojis", mp.TplEmojis},
		{&mp.stickers, "stickers", mp.TplStickers},
		{&mp.avatars, "avatars", mp.TplAvatars},
	}
	for _, pair := range pairs {
		if pair.template == "" {
			continue
		}
		*pair.ptr, err = tpl.New(pair.name).Parse(pair.template)
		if err != nil {
			return err
		}
	}
	return nil
}

type attachmentParams struct {
	ChannelID    string
	AttachmentID string
	FileName     string
}

type emojiStickerParams struct {
	ID  string
	Ext string
}

type avatarParams struct {
	UserID   string
	AvatarID string
	Ext      string
}

func (mp *MediaPatterns) execute(tpl *template.Template, params any) id.ContentURI {
	if tpl == nil || !mp.Enabled {
		return id.ContentURI{}
	}
	var out strings.Builder
	err := tpl.Execute(&out, params)
	if err != nil {
		panic(err)
	}
	uri, err := id.ParseContentURI(out.String())
	if err != nil {
		panic(err)
	}
	return uri
}

func (mp *MediaPatterns) Attachment(channelID, attachmentID, filename string) id.ContentURI {
	return mp.execute(mp.attachments, attachmentParams{
		ChannelID:    channelID,
		AttachmentID: attachmentID,
		FileName:     filename,
	})
}

func (mp *MediaPatterns) Emoji(emojiID, ext string) id.ContentURI {
	return mp.execute(mp.emojis, emojiStickerParams{
		ID:  emojiID,
		Ext: ext,
	})
}

func (mp *MediaPatterns) Sticker(stickerID, ext string) id.ContentURI {
	return mp.execute(mp.stickers, emojiStickerParams{
		ID:  stickerID,
		Ext: ext,
	})
}

func (mp *MediaPatterns) Avatar(userID, avatarID, ext string) id.ContentURI {
	return mp.execute(mp.avatars, avatarParams{
		UserID:   userID,
		AvatarID: avatarID,
		Ext:      ext,
	})
}

type BackfillLimitPart struct {
	DM      int `yaml:"dm"`
	Channel int `yaml:"channel"`
	Thread  int `yaml:"thread"`
}

func (bc *BridgeConfig) GetResendBridgeInfo() bool {
	return bc.ResendBridgeInfo
}

func (bc *BridgeConfig) EnableMessageStatusEvents() bool {
	return bc.MessageStatusEvents
}

func (bc *BridgeConfig) EnableMessageErrorNotices() bool {
	return bc.MessageErrorNotices
}

func boolToInt(val bool) int {
	if val {
		return 1
	}
	return 0
}

func (bc *BridgeConfig) Validate() error {
	_, hasWildcard := bc.Permissions["*"]
	_, hasExampleDomain := bc.Permissions["example.com"]
	_, hasExampleUser := bc.Permissions["@admin:example.com"]
	exampleLen := boolToInt(hasWildcard) + boolToInt(hasExampleUser) + boolToInt(hasExampleDomain)
	if len(bc.Permissions) <= exampleLen {
		return errors.New("bridge.permissions not configured")
	}
	return nil
}

type umBridgeConfig BridgeConfig

func (bc *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umBridgeConfig)(bc))
	if err != nil {
		return err
	}

	bc.usernameTemplate, err = template.New("username").Parse(bc.UsernameTemplate)
	if err != nil {
		return err
	} else if !strings.Contains(bc.FormatUsername("1234567890"), "1234567890") {
		return fmt.Errorf("username template is missing user ID placeholder")
	}
	bc.displaynameTemplate, err = template.New("displayname").Parse(bc.DisplaynameTemplate)
	if err != nil {
		return err
	}
	bc.channelNameTemplate, err = template.New("channel_name").Parse(bc.ChannelNameTemplate)
	if err != nil {
		return err
	}
	bc.guildNameTemplate, err = template.New("guild_name").Parse(bc.GuildNameTemplate)
	if err != nil {
		return err
	}

	return nil
}

var _ bridgeconfig.BridgeConfig = (*BridgeConfig)(nil)

func (bc BridgeConfig) GetEncryptionConfig() bridgeconfig.EncryptionConfig {
	return bc.Encryption
}

func (bc BridgeConfig) GetCommandPrefix() string {
	return bc.CommandPrefix
}

func (bc BridgeConfig) GetManagementRoomTexts() bridgeconfig.ManagementRoomTexts {
	return bc.ManagementRoomText
}

func (bc BridgeConfig) FormatUsername(userID string) string {
	var buffer strings.Builder
	_ = bc.usernameTemplate.Execute(&buffer, userID)
	return buffer.String()
}

type DisplaynameParams struct {
	*discordgo.User
	Webhook bool
}

func (bc BridgeConfig) FormatDisplayname(user *discordgo.User, webhook bool) string {
	var buffer strings.Builder
	_ = bc.displaynameTemplate.Execute(&buffer, &DisplaynameParams{user, webhook})
	return buffer.String()
}

type ChannelNameParams struct {
	Name       string
	ParentName string
	GuildName  string
	NSFW       bool
	Type       discordgo.ChannelType
}

func (bc BridgeConfig) FormatChannelName(params ChannelNameParams) string {
	var buffer strings.Builder
	_ = bc.channelNameTemplate.Execute(&buffer, params)
	return buffer.String()
}

type GuildNameParams struct {
	Name string
}

func (bc BridgeConfig) FormatGuildName(params GuildNameParams) string {
	var buffer strings.Builder
	_ = bc.guildNameTemplate.Execute(&buffer, params)
	return buffer.String()
}
