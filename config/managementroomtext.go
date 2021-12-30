package config

type managementRoomText struct {
	Welcome        string `yaml:"welcome"`
	Connected      string `yaml:"welcome_connected"`
	NotConnected   string `yaml:"welcome_unconnected"`
	AdditionalHelp string `yaml:"additional_help"`
}

func (m *managementRoomText) validate() error {
	if m.Welcome == "" {
		m.Welcome = "Greetings, I am a Discord bridge bot!"
	}

	if m.Connected == "" {
		m.Connected = "Use `help` to get started."
	}

	if m.NotConnected == "" {
		m.NotConnected = "Use `help` to get started, or `login` to login."
	}

	return nil
}

func (m *managementRoomText) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawManagementRoomText managementRoomText

	raw := rawManagementRoomText{}

	if err := unmarshal(&raw); err != nil {
		return err
	}

	*m = managementRoomText(raw)

	return m.validate()
}
