package config

import (
	"bytes"
	"text/template"
)

type bridge struct {
	UsernameTemplate string `yaml:"username_template"`

	usernameTemplate *template.Template `yaml:"-"`
}

func (b *bridge) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawBridge bridge

	raw := rawBridge{}

	err := unmarshal(&raw)
	if err != nil {
		return err
	}

	raw.usernameTemplate, err = template.New("username").Parse(raw.UsernameTemplate)
	if err != nil {
		return err
	}

	*b = bridge(raw)

	return nil
}

func (b bridge) FormatUsername(userid string) string {
	var buffer bytes.Buffer

	b.usernameTemplate.Execute(&buffer, userid)

	return buffer.String()
}
