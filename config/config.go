package config

import (
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Homeserver homeserver `yaml:"homeserver"`
	Appservice appservice `yaml:"appservice"`
	Bridge     bridge     `yaml:"bridge"`
}

func FromBytes(data []byte) (*Config, error) {
	cfg := Config{}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func FromString(str string) (*Config, error) {
	return FromBytes([]byte(str))
}

func FromFile(filename string) (*Config, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	return FromBytes(data)
}

func (cfg *Config) Save(filename string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filename, data, 0600)
}
