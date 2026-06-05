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

package discordauth

// Creds are some credentials that you use to initiate a login to Discord.
//
// This isn't all that is needed to log in successfully; you may have to solve
// a CAPTCHA, verify your login location, participate in MFA, etc.
type Creds struct {
	GiftCodeSKUID *string           `json:"gift_code_sku_id"`
	Login         string            `json:"login"`
	LoginSource   *string           `json:"login_source"`
	Password      Sensitive[string] `json:"password"`
	Undelete      bool              `json:"undelete"`
}

func NewCreds(emailOrPhone string, password string) *Creds {
	return &Creds{
		Login:    emailOrPhone,
		Password: NewSensitive(password),
		Undelete: false,
	}
}

// A LoginCompleted is returned from Discord when a log in flow terminates in
// success. This includes the presence of any involved MFA flows.
type LoginCompleted struct {
	Token           Sensitive[string] `json:"token"`
	UserID          string            `json:"user_id"`
	UserSettings    UserSettings      `json:"user_settings"`
	RequiredActions []string          `json:"required_actions"`
}

func (lc *LoginCompleted) HasToken() bool {
	return !lc.Token.IsZero()
}

type UserSettings struct {
	Locale string `json:"locale"`
	Theme  string `json:"theme"`
}
