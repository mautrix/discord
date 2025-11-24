// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	LoginFlowIDToken = "fi.mau.discord.login.token"
)

func (d *DiscordConnector) GetLoginFlows() []bridgev2.LoginFlow {
	// FIXME(skip): Provide actually user-friendly login flows.
	return []bridgev2.LoginFlow{
		{
			ID:          LoginFlowIDToken,
			Name:        "Token",
			Description: "Provide a Discord user token to connect with.",
		},
	}
}

func (d *DiscordConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDToken {
		return nil, fmt.Errorf("unknown login flow ID")
	}

	return &DiscordLogin{User: user}, nil
}

type DiscordLogin struct {
	User    *bridgev2.User
	Token   string
	Session *discordgo.Session
}

var _ bridgev2.LoginProcessUserInput = (*DiscordLogin)(nil)

func (dl *DiscordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: "fi.mau.discord.enter_token",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   "token",
					Name: "Discord user account token",
					// Cribbed from https://regex101.com/r/1GMR0y/1.
					Pattern: `^(mfa\.[a-z0-9_-]{20,})|([a-z0-9_-]{23,28}\.[a-z0-9_-]{6,7}\.[a-z0-9_-]{27})$`,
				},
			},
		},
	}, nil
}

func (dl *DiscordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token := input["token"]
	if token == "" {
		return nil, fmt.Errorf("no token provided")
	}

	log := zerolog.Ctx(ctx)

	log.Info().Msg("Creating session from provided token")
	dl.Token = token

	session, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("couldn't create discord session: %w", err)
	}

	// FIXME(skip): Implement.
	session.EventHandler = func(evt any) {}

	// Set up logging.
	session.LogLevel = discordgo.LogInformational
	session.Logger = func(msgL, caller int, format string, a ...any) {
		// FIXME(skip): Hook up zerolog properly.
		log.Debug().Str("component", "discordgo").Msgf(strings.TrimSpace(format), a...) // zerolog-allow-msgf
	}

	cl := DiscordClient{
		Session: session,
	}
	err = cl.connect(ctx)
	if err != nil {
		dl.softlyCloseSession()
		return nil, err
	}
	// At this point we've opened a WebSocket connection to the gateway, received
	// a READY packet, and know who we are.
	user := session.State.User

	dl.Session = session
	ul, err := dl.User.NewLogin(ctx, &database.UserLogin{
		ID: networkid.UserLoginID(user.ID),
		Metadata: &UserLoginMetadata{
			Token:            token,
			HeartbeatSession: session.HeartbeatSession,
		},
	}, &bridgev2.NewLoginParams{
		// We already have a Session; call this instead of the connector's main LoadUserLogin method and thread the Session through.
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			login.Client = &cl
			return nil
		},
		DeleteOnConflict:  true,
		DontReuseExisting: false,
	})
	if err != nil {
		dl.softlyCloseSession()
		return nil, fmt.Errorf("couldn't create login: %w", err)
	}
	zerolog.Ctx(ctx).Info().Str("user_id", user.ID).Str("user_username", user.Username).Msg("Connected to Discord during login")

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "fi.mau.discord.complete",
		Instructions: fmt.Sprintf("Logged in as %s", user),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

func (dl *DiscordLogin) softlyCloseSession() {
	log.Debug().Msg("Closing session")
	err := dl.Session.Close()
	if err != nil {
		log.Err(err).Msg("Couldn't close Discord session in response to login cancellation")
	}
}

func (dl *DiscordLogin) Cancel() {
	dl.softlyCloseSession()
}
