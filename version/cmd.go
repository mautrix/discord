package version

import (
	"fmt"

	"go.mau.fi/mautrix-discord/consts"
	"go.mau.fi/mautrix-discord/globals"
)

type Cmd struct{}

func (c *Cmd) Run(g *globals.Globals) error {
	fmt.Printf("%s %s\n", consts.Name, String)

	return nil
}
