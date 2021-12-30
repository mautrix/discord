package config

import (
	"bytes"
	"text/template"
)

type bridge struct {
	UsernameTemplate string `yaml:"username_template"`

	CommandPrefix string `yaml:"command_prefix"`

	ManagementRoomText managementRoomText `yaml:"management_root_text"`

	PortalMessageBuffer int `yaml:"portal_message_buffer"`

	usernameTemplate *template.Template `yaml:"-"`
}

func (b *bridge) validate() error {
	var err error

	if b.UsernameTemplate == "" {
		b.UsernameTemplate = "Discord_{{.}}"
	}

	if b.PortalMessageBuffer <= 0 {
		b.PortalMessageBuffer = 128
	}

	b.usernameTemplate, err = template.New("username").Parse(b.UsernameTemplate)
	if err != nil {
		return err
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
	var buffer bytes.Buffer

	b.usernameTemplate.Execute(&buffer, userid)

	return buffer.String()
}
