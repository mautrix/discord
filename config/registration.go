package config

import (
	"fmt"
	"regexp"

	as "maunium.net/go/mautrix/appservice"
)

func (cfg *Config) CopyToRegistration(registration *as.Registration) error {
	registration.ID = cfg.Appservice.ID
	registration.URL = cfg.Appservice.Address
	registration.EphemeralEvents = cfg.Appservice.EphemeralEvents

	falseVal := false
	registration.RateLimited = &falseVal

	registration.SenderLocalpart = cfg.Appservice.Bot.Username

	pattern := fmt.Sprintf(
		"^@%s:%s$",
		cfg.Bridge.FormatUsername("[0-9]+"),
		cfg.Homeserver.Domain,
	)

	userIDRegex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	registration.Namespaces.RegisterUserIDs(userIDRegex, true)

	return nil
}

func (cfg *Config) getRegistration() (*as.Registration, error) {
	registration := as.CreateRegistration()

	if err := cfg.CopyToRegistration(registration); err != nil {
		return nil, err
	}

	registration.AppToken = cfg.Appservice.ASToken
	registration.ServerToken = cfg.Appservice.HSToken

	return registration, nil
}
