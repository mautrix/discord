package config

import (
	"strings"

	as "maunium.net/go/mautrix/appservice"
)

type provisioning struct {
	Prefix       string `yaml:"prefix"`
	SharedSecret string `yaml:"shared_secret"`
}

func (p *provisioning) validate() error {
	if p.Prefix == "" {
		p.Prefix = "/_matrix/provision/v1"
	}

	if strings.ToLower(p.SharedSecret) == "generate" {
		p.SharedSecret = as.RandomString(64)

		configUpdated = true
	}

	return nil
}

func (p *provisioning) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawProvisioning provisioning

	raw := rawProvisioning{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*p = provisioning(raw)

	return p.validate()
}

func (p *provisioning) Enabled() bool {
	return strings.ToLower(p.SharedSecret) != "disable"
}
