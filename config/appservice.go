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

func (a *appservice) setDefaults() error {
	if a.ID == "" {
		a.ID = "discord"
	}

	if err := a.Bot.setDefaults(); err != nil {
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

	return a.setDefaults()
}
