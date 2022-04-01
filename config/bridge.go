package config

import (
	"strings"
	"text/template"

	"maunium.net/go/mautrix/id"

	"github.com/bwmarrin/discordgo"
)

type bridge struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`

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
