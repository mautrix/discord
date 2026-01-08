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

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const LoginFlowIDToken = "fi.mau.discord.login.token"

type DiscordTokenLogin struct {
	connector *DiscordConnector
	User      *bridgev2.User
	Token     string
	Session   *discordgo.Session
}

var _ bridgev2.LoginProcessUserInput = (*DiscordTokenLogin)(nil)

func (dl *DiscordTokenLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
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

func (dl *DiscordTokenLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token := input["token"]
	if token == "" {
		return nil, fmt.Errorf("no token provided")
	}

	log := zerolog.Ctx(ctx)

	log.Info().Msg("Creating session from provided token")
	dl.Token = token

	session, err := NewDiscordSession(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("couldn't create discord session: %w", err)
	}

	client := DiscordClient{
		connector: dl.connector,
		Session:   session,
	}
	client.SetUp(ctx, nil)
	err = client.connect(ctx)
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
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			login.Client = &client
			client.UserLogin = login

			// Only now that we have a UserLogin can we begin syncing.
			client.BeginSyncingIfUserLoginPresent(ctx)
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
		StepID:       LoginStepIDComplete,
		Instructions: fmt.Sprintf("Logged in as %s", user),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

func (dl *DiscordTokenLogin) softlyCloseSession() {
	dl.User.Log.Debug().Msg("Closing session")
	err := dl.Session.Close()
	if err != nil {
		dl.User.Log.Err(err).Msg("Couldn't close Discord session in response to login cancellation")
	}
}

func (dl *DiscordTokenLogin) Cancel() {
	dl.softlyCloseSession()
}
