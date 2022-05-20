package registration

import (
	"fmt"
	"os"
	"regexp"

	"maunium.net/go/mautrix/appservice"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/globals"
)

type Cmd struct {
	Filename string `kong:"flag,help='The filename to store the registration into',name='REGISTRATION',short='r',default='registration.yaml'"`
	Force    bool   `kong:"flag,help='Overwrite an existing registration file if it already exists',short='f',default='0'"`
}

func (c *Cmd) Run(g *globals.Globals) error {
	// Check if the file exists before blinding overwriting it.
	if _, err := os.Stat(c.Filename); err == nil {
		if c.Force == false {
			return fmt.Errorf("file %q exists, use -f to overwrite", c.Filename)
		}
	}

	cfg, err := config.FromFile(g.Config)
	if err != nil {
		return err
	}

	registration := appservice.CreateRegistration()

	// Load existing values from the config into the registration.
	if err := cfg.CopyToRegistration(registration); err != nil {
		return err
	}

	// Save the new App and Server tokens in the config.
	cfg.Appservice.ASToken = registration.AppToken
	cfg.Appservice.HSToken = registration.ServerToken

	// Workaround for https://github.com/matrix-org/synapse/pull/5758
	registration.SenderLocalpart = appservice.RandomString(32)

	// Register the bot's user.
	pattern := fmt.Sprintf(
		"^@%s:%s$",
		cfg.Appservice.Bot.Username,
		cfg.Homeserver.Domain,
	)
	botRegex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	registration.Namespaces.RegisterUserIDs(botRegex, true)

	// Finally save the registration and the updated config file.
	if err := registration.Save(c.Filename); err != nil {
		return err
	}

	if err := cfg.Save(g.Config); err != nil {
		return err
	}

	return nil
}
