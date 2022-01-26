package config

import (
	"strings"
	"text/template"

	"github.com/bwmarrin/discordgo"
)

type bridge struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`

	CommandPrefix string `yaml:"command_prefix"`

	ManagementRoomText managementRoomText `yaml:"management_root_text"`

	PortalMessageBuffer int `yaml:"portal_message_buffer"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
}

func (b *bridge) validate() error {
	var err error

	if b.UsernameTemplate == "" {
		b.UsernameTemplate = "Discord_{{.}}"
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

	raw := rawBridge{}

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
