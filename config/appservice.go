package config

type appservice struct {
	Address  string `yaml:"address"`
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`

	ASToken string `yaml:"as_token"`
	HSToken string `yaml:"hs_token"`
}
