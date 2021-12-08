package config

import (
	as "maunium.net/go/mautrix/appservice"
)

type appservice struct {
	Address  string `yaml:"address"`
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`

	ID string `yaml:"id"`

	Bot bot `yaml:"bot"`

	ASToken string `yaml:"as_token"`
	HSToken string `yaml:"hs_token"`
}

func (a *appservice) validate() error {
	if a.ID == "" {
		a.ID = "discord"
	}

	if a.Address == "" {
		a.Address = "http://localhost:29350"
	}

	if a.Hostname == "" {
		a.Hostname = "0.0.0.0"
	}

	if a.Port == 0 {
		a.Port = 29350
	}

	if err := a.Bot.validate(); err != nil {
		return err
	}

	return nil
}

func (a *appservice) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawAppservice appservice

	raw := rawAppservice{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*a = appservice(raw)

	return a.validate()
}

func (cfg *Config) CreateAppService() (*as.AppService, error) {
	appservice := as.Create()

	appservice.HomeserverURL = cfg.Homeserver.Address
	appservice.HomeserverDomain = cfg.Homeserver.Domain

	appservice.Host.Hostname = cfg.Appservice.Hostname
	appservice.Host.Port = cfg.Appservice.Port
	appservice.DefaultHTTPRetries = 4

	reg, err := cfg.getRegistration()
	if err != nil {
		return nil, err
	}

	appservice.Registration = reg

	return appservice, nil
}
