package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"

	"gitlab.com/beeper/discord/config"
	"gitlab.com/beeper/discord/consts"
	"gitlab.com/beeper/discord/globals"
	"gitlab.com/beeper/discord/registration"
	"gitlab.com/beeper/discord/version"
)

var cli struct {
	globals.Globals

	GenerateConfig       config.Cmd       `kong:"cmd,help='Generate the default configuration and exit.'"`
	GenerateRegistration registration.Cmd `kong:"cmd,help='Generate the registration file for synapse and exit.'"`
	Version              version.Cmd      `kong:"cmd,help='Display the version and exit.'"`
}

func main() {
	ctx := kong.Parse(
		&cli,
		kong.Name(consts.Name),
		kong.Description(consts.Description),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			Summary: true,
		}),
	)

	err := ctx.Run(&cli.Globals)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
