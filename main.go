package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/consts"
	"go.mau.fi/mautrix-discord/globals"
	"go.mau.fi/mautrix-discord/registration"
	"go.mau.fi/mautrix-discord/run"
	"go.mau.fi/mautrix-discord/version"
)

var cli struct {
	globals.Globals

	GenerateConfig       config.Cmd       `kong:"cmd,help='Generate the default configuration and exit.'"`
	GenerateRegistration registration.Cmd `kong:"cmd,help='Generate the registration file for synapse and exit.'"`
	Run                  run.Cmd          `kong:"cmd,help='Run the bridge.',default='1'"`
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
