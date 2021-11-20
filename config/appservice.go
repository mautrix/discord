package config

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
