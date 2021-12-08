package run

import (
	"os"
	"os/signal"
	"syscall"

	"gitlab.com/beeper/discord/bridge"
	"gitlab.com/beeper/discord/config"
	"gitlab.com/beeper/discord/globals"
)

type Cmd struct{}

func (c *Cmd) Run(g *globals.Globals) error {
	cfg, err := config.FromFile(g.Config)
	if err != nil {
		return err
	}

	bridge, err := bridge.New(cfg)
	if err != nil {
		return err
	}

	if err := bridge.Start(); err != nil {
		return err
	}

	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch

	bridge.Stop()

	return nil
}
