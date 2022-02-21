package config

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Homeserver homeserver `yaml:"homeserver"`
	Appservice appservice `yaml:"appservice"`
	Bridge     bridge     `yaml:"bridge"`
	Logging    logging    `yaml:"logging"`

	filename string `yaml:"-"`
}

var configUpdated bool

func (cfg *Config) validate() error {
	if err := cfg.Homeserver.validate(); err != nil {
		return err
	}

	if err := cfg.Appservice.validate(); err != nil {
		return err
	}

	if err := cfg.Bridge.validate(); err != nil {
		return err
	}

	if err := cfg.Logging.validate(); err != nil {
		return err
	}

	if configUpdated {
		return cfg.Save(cfg.filename)
	}

	return nil
}

func (cfg *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawConfig Config

	raw := rawConfig{
		filename: cfg.filename,
	}

	if err := unmarshal(&raw); err != nil {
		return err
	}

	*cfg = Config(raw)

	return cfg.validate()
}

func FromBytes(filename string, data []byte) (*Config, error) {
	cfg := Config{
		filename: filename,
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func FromString(str string) (*Config, error) {
	return FromBytes("", []byte(str))
}

func FromFile(filename string) (*Config, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	return FromBytes(filename, data)
}

func (cfg *Config) Save(filename string) error {
	if filename == "" {
		return fmt.Errorf("no filename specified yep")
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filename, data, 0600)
}
