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

import "context"

// An AuthenticatorType is what you append to "/auth/mfa/" to respond to an MFA
// challenge with that MFA method.
//
// For example, to use a TOTP code, you'd POST to "/auth/mfa/totp".
// [AuthenticatorTOTP] is "totp".
type AuthenticatorType string

const (
	AuthenticatorTOTP     AuthenticatorType = "totp"
	AuthenticatorSMS      AuthenticatorType = "sms"
	AuthenticatorBackup   AuthenticatorType = "backup"
	AuthenticatorWebAuthn AuthenticatorType = "webauthn"
	AuthenticatorPassword AuthenticatorType = "password"
)

// MFAChallenge encapsulates the context regarding an in-progress MFA flow. Use
// the data from this struct to inform how you will proceed with
// authentication.
//
// This is received by a [ChallengeHandler].
type MFAChallenge struct {
	*LoginMFARequired

	// RequestSMS asks Discord to send a MFA code to the user's phone number.
	// This will only work if the user has SMS MFA enabled.
	RequestSMS func(context.Context) (*SMSSendResponse, error)

	// PreviousError is present when a prior code submission in this challenge
	// was rejected by Discord's backend. Handlers should surface this to the
	// user interface as part of a retry, e.g. "that code was invalid, try
	// again".
	PreviousError error
}

// An [MFAContinue] combines an [AuthenticatorType] with an [MFAContinuation],
// which lets the [AuthMachine] know how to make the HTTP request to Discord.
//
// This is returned out of a [ChallengeHandler] when the client is ready to let
// the library know how to proceed with the MFA log in flow.
type MFAContinue struct {
	Type AuthenticatorType
	MFAContinuation
}

// An MFAState encapsulates the essential, opaque data that is received from
// Discord when MFA is required to proceed with a log in. This data must be
// sent back as part of your MFA response ([MFAContinuation]).
//
// This struct exists solely for organizational purposes.
type MFAState struct {
	Ticket          Sensitive[string] `json:"ticket"`
	LoginInstanceID string            `json:"login_instance_id"`
}

// A LoginMFARequired is returned from Discord's login endpoint when the
// password is accepted, but another authentication factor is required.
type LoginMFARequired struct {
	MFAState

	UserID              string  `json:"user_id"`
	MFARequired         bool    `json:"mfa"`    // multi-factor authentication is required to log in
	SMSEnabled          bool    `json:"sms"`    // whether SMS-based MFA is enabled
	BackupCodesAccepted bool    `json:"backup"` // whether backup codes can be used in the response
	TOTPEnabled         bool    `json:"totp"`
	WebAuthnCredential  *string `json:"webauthn"` // JSON string of {"publicKey": {"challenge": ...}}
}

// POST an MFAContinuation to Discord upon receiving a [LoginMFARequired] and
// you have the necessary code (TOTP, SMS, backup, WebAuthn, etc.) to continue.
type MFAContinuation struct {
	MFAState

	// The TOTP, SMS code, backup code, or Webauthn credential used to complete
	// the MFA flow.
	//
	// Backup codes are displayed hyphenated in Discord's UI, which visually
	// splits them in half. Discord's API will not accept backup codes with the
	// hyphens intact, so they must be stripped before submission.
	Code string `json:"code"`

	GiftCodeSKUID *string `json:"gift_code_sku_id"`
	LoginSource   *string `json:"login_source"`
}

// POST an SMSSendRequest to Discord upon receiving a [LoginMFARequired] if SMS
// is a permitted MFA path and you'd like to send an SMS code to the user.
type SMSSendRequest struct {
	Ticket Sensitive[string] `json:"ticket"`
}

// SMSSendResponse is what Discord returns from /auth/mfa/sms/send.
type SMSSendResponse struct {
	Phone string `json:"phone"` // partially redacted phone number
}
