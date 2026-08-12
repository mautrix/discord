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

	"maunium.net/go/mautrix/bridgev2"
)

const LoginStepIDComplete = "fi.mau.discord.login.complete"

func (d *DiscordConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			ID:          LoginFlowIDBrowser,
			Name:        "Browser",
			Description: "Log in to your Discord account in a web browser.",
		},
		{
			ID:          LoginFlowIDRemoteAuth,
			Name:        "QR Code",
			Description: "Scan a QR code with the Discord mobile app to log in.",
		},
		{
			ID:          LoginFlowIDToken,
			Name:        "Token",
			Description: "Provide a Discord user token to connect with.",
		},
		{
			ID:          LoginFlowIDMachine,
			Name:        "Email/Phone & Password",
			Description: "Log in with an email or phone number and a password. Supports multi-factor authentication (e.g. TOTP, SMS, etc.)",
		},
	}
}

func (d *DiscordConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	login := DiscordGenericLogin{
		connector: d,
		User:      user,
	}

	switch flowID {
	case LoginFlowIDToken:
		return &DiscordTokenLogin{DiscordGenericLogin: &login}, nil
	case LoginFlowIDRemoteAuth:
		return &DiscordRemoteAuthLogin{DiscordGenericLogin: &login}, nil
	case LoginFlowIDBrowser:
		return &DiscordBrowserLogin{DiscordGenericLogin: &login}, nil
	case LoginFlowIDMachine:
		mach, err := NewDiscordMachineLogin(ctx, &login)
		if err != nil {
			return nil, fmt.Errorf("failed to set up discord login machine: %w", err)
		}

		return mach, nil
	default:
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
}
