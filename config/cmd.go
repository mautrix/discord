package config

import (
	"gitlab.com/beeper/discord/globals"
)

type Cmd struct {
	HomeserverAddress string `kong:"arg,help='The url to for the homeserver',required='1'"`
	Domain            string `kong:"arg,help='The domain for the homeserver',required='1'"`
}

func (c *Cmd) Run(g *globals.Globals) error {
	cfg := &Config{
		Homeserver: homeserver{
			Address: c.HomeserverAddress,
			Domain:  c.Domain,
		},
	}

	if err := cfg.validate(); err != nil {
		return err
	}

	return cfg.Save(g.Config)
}
