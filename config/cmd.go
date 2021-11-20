package config

import (
	"fmt"
	"os"

	"gitlab.com/beeper/discord/globals"
)

type Cmd struct {
	HomeserverAddress string `kong:"arg,help='The url to for the homeserver',required='1'"`
	Domain            string `kong:"arg,help='The domain for the homeserver',required='1'"`

	Force bool `kong:"flag,help='Overwrite an existing configuration file if one already exists',short='f',default='0'"`
}

func (c *Cmd) Run(g *globals.Globals) error {
	if _, err := os.Stat(g.Config); err == nil {
		if c.Force == false {
			return fmt.Errorf("file %q exists, use -f to overwrite", g.Config)
		}
	}

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
