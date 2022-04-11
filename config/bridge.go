package config

import (
	"fmt"
	"strings"
	"text/template"

	"maunium.net/go/mautrix/id"

	"github.com/bwmarrin/discordgo"
)

type bridge struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`
	ChannelnameTemplate string `yaml:"channelname_template"`

	CommandPrefix string `yaml:"command_prefix"`

	ManagementRoomText managementRoomText `yaml:"management_root_text"`

	PortalMessageBuffer int `yaml:"portal_message_buffer"`

	SyncWithCustomPuppets bool `yaml:"sync_with_custom_puppets"`
	SyncDirectChatList    bool `yaml:"sync_direct_chat_list"`
	DefaultBridgeReceipts bool `yaml:"default_bridge_receipts"`
	DefaultBridgePresence bool `yaml:"default_bridge_presence"`

	DoublePuppetServerMap      map[string]string `yaml:"double_puppet_server_map"`
	DoublePuppetAllowDiscovery bool              `yaml:"double_puppet_allow_discovery"`
	LoginSharedSecretMap       map[string]string `yaml:"login_shared_secret_map"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
	channelnameTemplate *template.Template `yaml:"-"`
}

func (config *Config) CanAutoDoublePuppet(userID id.UserID) bool {
	_, homeserver, _ := userID.Parse()
	_, hasSecret := config.Bridge.LoginSharedSecretMap[homeserver]

	return hasSecret
}

func (b *bridge) validate() error {
	var err error

	if b.UsernameTemplate == "" {
		b.UsernameTemplate = "discord_{{.}}"
	}

	b.usernameTemplate, err = template.New("username").Parse(b.UsernameTemplate)
	if err != nil {
		return err
	}

	if b.DisplaynameTemplate == "" {
		b.DisplaynameTemplate = "{{.Username}}#{{.Discriminator}} (D){{if .Bot}} (bot){{end}}"
	}

	b.displaynameTemplate, err = template.New("displayname").Parse(b.DisplaynameTemplate)
	if err != nil {
		return err
	}

	if b.ChannelnameTemplate == "" {
		b.ChannelnameTemplate = "{{if .Guild}}{{.Guild}} - {{end}}{{if .Folder}}{{.Folder}} - {{end}}{{.Name}} (D)"
	}

	b.channelnameTemplate, err = template.New("channelname").Parse(b.ChannelnameTemplate)
	if err != nil {
		return err
	}

	if b.PortalMessageBuffer <= 0 {
		b.PortalMessageBuffer = 128
	}

	if b.CommandPrefix == "" {
		b.CommandPrefix = "!dis"
	}

	if err := b.ManagementRoomText.validate(); err != nil {
		return err
	}

	return nil
}

func (b *bridge) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawBridge bridge

	// Set our defaults that aren't zero values.
	raw := rawBridge{
		SyncWithCustomPuppets: true,
		DefaultBridgeReceipts: true,
		DefaultBridgePresence: true,
	}

	err := unmarshal(&raw)
	if err != nil {
		return err
	}

	*b = bridge(raw)

	return b.validate()
}

func (b bridge) FormatUsername(userid string) string {
	var buffer strings.Builder

	b.usernameTemplate.Execute(&buffer, userid)

	return buffer.String()
}

type simplfiedUser struct {
	Username      string
	Discriminator string
	Locale        string
	Verified      bool
	MFAEnabled    bool
	Bot           bool
	System        bool
}

func (b bridge) FormatDisplayname(user *discordgo.User) string {
	var buffer strings.Builder

	b.displaynameTemplate.Execute(&buffer, simplfiedUser{
		Username:      user.Username,
		Discriminator: user.Discriminator,
		Locale:        user.Locale,
		Verified:      user.Verified,
		MFAEnabled:    user.MFAEnabled,
		Bot:           user.Bot,
		System:        user.System,
	})

	return buffer.String()
}

type simplfiedChannel struct {
	Guild  string
	Folder string
	Name   string
	NSFW   bool
}

func (b bridge) FormatChannelname(channel *discordgo.Channel, session *discordgo.Session) (string, error) {
	var buffer strings.Builder
	var guildName, folderName string

	if channel.Type != discordgo.ChannelTypeDM && channel.Type != discordgo.ChannelTypeGroupDM {
		guild, err := session.Guild(channel.GuildID)
		if err != nil {
			return "", fmt.Errorf("find guild: %w", err)
		}
		guildName = guild.Name

		folder, err := session.Channel(channel.ParentID)
		if err == nil {
			folderName = folder.Name
		}
	} else {
		// Group DM's can have a name, but DM's can't, so if we didn't get a
		// name return a comma separated list of the formatted user names.
		if channel.Name == "" {
			recipients := make([]string, len(channel.Recipients))
			for idx, user := range channel.Recipients {
				recipients[idx] = b.FormatDisplayname(user)
			}

			return strings.Join(recipients, ", "), nil
		}
	}

	b.channelnameTemplate.Execute(&buffer, simplfiedChannel{
		Guild:  guildName,
		Folder: folderName,
		Name:   channel.Name,
		NSFW:   channel.NSFW,
	})

	return buffer.String(), nil
}
