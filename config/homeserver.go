package config

import (
	"errors"
)

var (
	ErrHomeserverNoAddress = errors.New("no homeserver address specified")
	ErrHomeserverNoDomain  = errors.New("no homeserver domain specified")
)

type homeserver struct {
	Address        string `yaml:"address"`
	Domain         string `yaml:"domain"`
	Asmux          bool   `yaml:"asmux"`
	StatusEndpoint string `yaml:"status_endpoint"`
}

func (h *homeserver) validate() error {
	if h.Address == "" {
		return ErrHomeserverNoAddress
	}

	if h.Domain == "" {
		return ErrHomeserverNoDomain
	}

	return nil
}

func (h *homeserver) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawHomeserver homeserver

	raw := rawHomeserver{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*h = homeserver(raw)

	return h.validate()
}
