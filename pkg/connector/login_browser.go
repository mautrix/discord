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

const LoginFlowIDBrowser = "fi.mau.discord.login.browser"

type DiscordBrowserLogin struct {
	connector *DiscordConnector
	User      *bridgev2.User

	Session *discordgo.Session
}

var _ bridgev2.LoginProcessCookies = (*DiscordBrowserLogin)(nil)

func (dl *DiscordBrowserLogin) softlyCloseSession() {
	dl.User.Log.Debug().Msg("Closing session")
	err := dl.Session.Close()
	if err != nil {
		dl.User.Log.Err(err).Msg("Couldn't close Discord session in response to login cancellation")
	}
}

func (dl *DiscordBrowserLogin) Cancel() {
}

const ExtractDiscordTokenJS = `
new Promise((resolve) => {
	let mautrixDiscordTokenCheckInterval

	const iframe = document.createElement('iframe')
	document.head.append(iframe)

	mautrixDiscordTokenCheckInterval = setInterval(() => {
	  const token = iframe.contentWindow.localStorage.token
	  if (token) {
		resolve({ token: token.slice(1, -1) })
		clearInterval(mautrixDiscordTokenCheckInterval)
	  }
	}, 200)
})
`

func (dl *DiscordBrowserLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       "fi.mau.discord.cookies",
		Instructions: "Log in with Discord.",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       "https://discord.com/login",
			UserAgent: "",
			Fields: []bridgev2.LoginCookieField{{
				ID:       "token",
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: "fi.mau.discord.token",
				}},
			}},
			ExtractJS: ExtractDiscordTokenJS,
		},
	}, nil
}

func (dl *DiscordBrowserLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	log := zerolog.Ctx(ctx)

	token := cookies["token"]
	if token == "" {
		log.Error().Msg("Received empty token")
		return nil, fmt.Errorf("received empty token")
	}
	log.Debug().Msg("Logging in with submitted cookie")

	// FIXME FIXME: The rest of this method is basically copy and pasted from
	// DiscordTokenLogin, so find a way to tidy this up.

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
