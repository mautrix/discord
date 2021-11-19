package config

type bot struct {
	Username    string `yaml:"username"`
	Displayname string `yaml:"displayname"`
	Avatar      string `yaml:"avatar"`
}

func (b *bot) validate() error {
	if b.Username == "" {
		b.Username = "discordbot"
	}

	if b.Displayname == "" {
		b.Displayname = "Discord Bridge Bot"
	}

	return nil
}

func (b *bot) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawBot bot

	raw := rawBot{}

	if err := unmarshal(&raw); err != nil {
		return err
	}

	*b = bot(raw)

	return b.validate()
}
