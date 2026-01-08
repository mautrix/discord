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

	"maunium.net/go/mautrix/bridgev2"
)

const LoginFlowIDToken = "fi.mau.discord.login.token"

type DiscordTokenLogin struct {
	*DiscordGenericLogin
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

	ul, err := dl.FinalizeCreatingLogin(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("couldn't login from token: %w", err)
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
