// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

const LoginFlowIDBrowser = "fi.mau.discord.login.browser"

type DiscordBrowserLogin struct {
	*DiscordGenericLogin
}

var _ bridgev2.LoginProcessCookies = (*DiscordBrowserLogin)(nil)

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

	ul, err := dl.FinalizeCreatingLogin(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("couldn't log in via browser: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: dl.CompleteInstructions(),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
