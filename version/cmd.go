package version

import (
	"fmt"

	"gitlab.com/beeper/discord/consts"
	"gitlab.com/beeper/discord/globals"
)

type Cmd struct{}

func (c *Cmd) Run(g *globals.Globals) error {
	fmt.Printf("%s %s\n", consts.Name, String)

	return nil
}
