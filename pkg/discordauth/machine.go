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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"
)

// An AuthMachine is a resumable state machine that embodies the core logic to
// authenticate a user account with Discord. It is concerned with:
//
//   - Detecting CAPTCHA challenges, reporting them, and attaching the subsequent
//     solution to the retried request.
//   - Recognizing conditions such as IP verification and other required
//     actions.
//   - Accruing the state and fingerprints necessary to send the correct set of
//     headers with each HTTP request.
//
// The machine converses purely in terms of [Prompt] (what we need from the
// user) and [Answer] (what the user provided); it does not presuppose how this
// information is presented to the user.
//
// Construct one with [NewAuthMachine], call [AuthMachine.Prepare] once, then
// call [AuthMachine.Advance] repeatedly until a [LoginCompleted] is yielded,
// which signals completion.
//
// An AuthMachine is single-use and not safe for use by multiple goroutines.
type AuthMachine struct {
	log     *zerolog.Logger
	http    HTTP
	APIBase string

	Fingerprint    Fingerprint
	InstallationID string
	Personality    *Personality

	pending    *pendingRequest   // the current operation being attempted
	lastPrompt *Prompt           // the last prompt that was shown to the user
	mfa        *LoginMFARequired // set upon entering MFA flow
	finished   bool
}

func NewAuthMachine(
	ctx context.Context,
	http HTTP,
	personality *Personality,
) *AuthMachine {
	log := zerolog.Ctx(ctx).With().
		Str("component", "discord auth").
		Logger()

	return &AuthMachine{
		log:     &log,
		http:    http,
		APIBase: "https://discord.com/api/v9",

		Personality: personality,
	}
}

type CredsPrompt struct {
	Reason string
}
type MFACodePrompt struct {
	Type AuthenticatorType
}
type MFAChallengePrompt struct {
	*LoginMFARequired
	Reason string
}

// A Prompt is a request for user input or interaction; the [AuthMachine] is
// suspended until the user's response is fed back to [AuthMachine.Advance] as
// an [Answer]. Exactly one field is set (non-nil or non-zero).
//
// Most prompts are ordinary steps of the login flow, but Captcha and
// EmailVerify are interruptions: they preempt whatever operation was in
// flight, and answering them ends up retrying it instead of starting a new
// one.
type Prompt struct {
	// CredsPrompt is non-nil when we need to prompt for the user's email/phone
	// number.
	CredsPrompt *CredsPrompt
	// Captcha is non-nil when a CAPTCHA challenge must be solved before
	// proceeding.
	Captcha *Captcha
	// MFAChallengePrompt is non-nil when the initial credentials were
	// accepted, but MFA is required to log in.
	//
	// Use the contained information to have the user select an authenticator
	// type to log in with (TOTP, backup code, or SMS).
	MFAChallengePrompt *MFAChallengePrompt
	// MFACodePrompt is non-nil when the selected authenticator type has been
	// noted and the state machine is ready to accept the MFA code (TOTP,
	// backup code, or SMS code).
	MFACodePrompt *MFACodePrompt
	// EmailVerify is true if an email requesting IP verification has been sent
	// to the email associated with the Discord account.
	EmailVerify bool
}

// A CaptchaSolution contains a solution to a [Captcha].
type CaptchaSolution struct {
	Solution string
}

// An Answer responds to the last [Prompt] returned from
// [AuthMachine.Advance]. Populate only the field corresponding to the field
// that was set on the prompt:
//
//   - [Prompt.CredsPrompt]: [Answer.Creds]
//   - [Prompt.Captcha]: [Answer.Solution]
//   - [Prompt.MFAChallengePrompt]: [Answer.PickedMFAType]
//   - [Prompt.MFACodePrompt]: [Answer.MFAContinue]
//   - [Prompt.EmailVerify]: none (an empty Answer resumes the flow)
type Answer struct {
	Creds         *Creds
	PickedMFAType *AuthenticatorType
	MFAContinue   *MFAContinue
	Solution      *CaptchaSolution
}

// Advance consumes an [Answer] to the previously returned [Prompt] (nil on
// the first call) and drives authentication forward, performing whatever
// Discord API requests are needed.
//
// When the error is nil, exactly one of the other return values is non-nil:
// the next [Prompt] to present, or a [LoginCompleted]. Once a [LoginCompleted]
// has been returned, the state machine has completed and further calls to
// Advance will error.
//
// An error does not advance the machine; it remains positioned at the last
// prompt.
func (am *AuthMachine) Advance(ctx context.Context, answer *Answer) (*Prompt, *LoginCompleted, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "advance discord auth state machine").
		Logger()
	ctx = log.WithContext(ctx)

	if am.finished {
		return nil, nil, fmt.Errorf("cannot advance finished auth machine")
	}

	prompt, completed, err := am.advance(ctx, answer)
	if prompt != nil {
		// Remembering which prompt was presented last lets us know what to
		// expect out of the next [Answer] (see [AuthMachine.advance]).
		am.lastPrompt = prompt
	}
	if completed != nil {
		am.finished = true
	}
	return prompt, completed, err
}

func (am *AuthMachine) advance(ctx context.Context, answer *Answer) (*Prompt, *LoginCompleted, error) {
	log := zerolog.Ctx(ctx)

	lastPrompt := am.lastPrompt
	if lastPrompt == nil {
		// Initial state; prompt for email/phone number and password.
		return &Prompt{CredsPrompt: &CredsPrompt{}}, nil, nil
	}

	var captchaSolution *CaptchaSolution
	switch {
	case lastPrompt.CredsPrompt != nil:
		// The user submitted their email/phone number and password.
		am.pending = loginOp(answer.Creds)
	case lastPrompt.MFAChallengePrompt != nil:
		// The user picked the MFA method they would like to proceed with.
		picked := *answer.PickedMFAType
		log.Info().Str("mfa_type", string(picked)).Msg("Continuing with MFA flow")

		if picked == AuthenticatorSMS {
			am.pending = sendSMSOp(&am.mfa.MFAState)
			// Fall into the pump to perform the request.
			break
		}
		return &Prompt{MFACodePrompt: &MFACodePrompt{
			Type: picked,
		}}, nil, nil
	case lastPrompt.MFACodePrompt != nil:
		// After having picked the authenticator type, the user inputted their
		// MFA code (the TOTP, backup code, or SMS code).
		am.pending = continueMFAOp(answer.MFAContinue, am.mfa)
	case lastPrompt.EmailVerify:
		// The user has authorized our IP address. Retry the last request.
	case lastPrompt.Captcha != nil:
		// The user solved the CAPTCHA challenge. Retry the last request.
		captchaSolution = answer.Solution
	default:
		return nil, nil, fmt.Errorf("cannot advance from unhandled prompt")
	}

	return am.pump(ctx, captchaSolution)
}

// pump executes the current [pendingRequest] while handling cross-cutting
// interrupts such as CAPTCHA challenges and email verification.
func (am *AuthMachine) pump(
	ctx context.Context,
	solution *CaptchaSolution,
) (*Prompt, *LoginCompleted, error) {
	log := zerolog.Ctx(ctx).With().
		Str("machine_op_name", am.pending.name).
		Logger()
	ctx = log.WithContext(ctx)

	// Build the request data.
	req, err := am.pending.request(ctx, am)
	if err != nil {
		return nil, nil, fmt.Errorf("constructing %s request: %w", am.pending.name, err)
	}
	// If we had just asked the user to solve a CAPTCHA challenge and a
	// solution was provided, add it to the HTTP request.
	if am.lastPrompt != nil && am.lastPrompt.Captcha != nil {
		if solution == nil {
			return nil, nil, fmt.Errorf("cannot proceed without a captcha solution")
		}
		req.Header.Set(HeaderCaptchaKey, solution.Solution)
		am.lastPrompt.Captcha.UpdateHeaders(&req.Header)
	}

	// Send the request to Discord.
	body, err := am.exchange(ctx, req)
	var capErr *CaptchaError
	var apiErr APIError
	switch {
	case errors.As(err, &capErr):
		return &Prompt{Captcha: capErr.Captcha}, nil, nil
	case errors.As(err, &apiErr):
		if apiErr.RequiresEmailVerification() {
			return &Prompt{EmailVerify: true}, nil, nil
		}
		// Give the current [pendingRequest] a chance to handle the error.
		if am.pending.fail != nil {
			prompt, err := am.pending.fail(ctx, am, apiErr)
			return prompt, nil, err
		}
		return nil, nil, apiErr
	case err != nil:
		return nil, nil, fmt.Errorf("making %s request: %w", am.pending.name, err)
	}

	prompt, done, err := am.pending.succeed(ctx, am, body)
	if err != nil || prompt != nil || done != nil {
		return prompt, done, err
	}

	return nil, nil, fmt.Errorf("%s operation did not advance auth state", am.pending.name)
}

// handleAuthResponse tries to consume a response body from /auth/login or
// /auth/mfa/<type> as a successful login, handling any encountered MFA
// challenge.
func (am *AuthMachine) handleAuthResponse(
	ctx context.Context,
	body []byte,
) (*Prompt, *LoginCompleted, error) {
	log := zerolog.Ctx(ctx)

	var completed LoginCompleted
	err := json.Unmarshal(body, &completed)
	if err != nil {
		err = fmt.Errorf("unmarshaling login response: %w", err)
		return nil, nil, err
	}

	if !completed.HasToken() {
		log.Debug().Msg("Response lacked a token, attempting to handle as MFA")

		am.mfa = &LoginMFARequired{}
		err = json.Unmarshal(body, &am.mfa)
		if err != nil {
			err = fmt.Errorf("unmarshaling mfa required response: %w", err)
			return nil, nil, err
		}
		if !am.mfa.MFARequired {
			// Discord responded with something else that we don't know
			// about yet.
			err = fmt.Errorf("unknown login response")
			return nil, nil, err
		}

		log := log.With().
			Str("mfa_login_instance_id", am.mfa.LoginInstanceID).
			Bool("mfa_accepting_backup_codes", am.mfa.BackupCodesAccepted).
			Bool("mfa_sms_enabled", am.mfa.SMSEnabled).
			Bool("mfa_totp_enabled", am.mfa.TOTPEnabled).
			Bool("mfa_has_webauthn_credential", am.mfa.WebAuthnCredential != nil).
			Logger()
		ctx = log.WithContext(ctx)

		log.Info().Msg("Need to log in with MFA")

		return &Prompt{MFAChallengePrompt: &MFAChallengePrompt{
			LoginMFARequired: am.mfa,
		}}, nil, nil
	}

	if am.mfa != nil && completed.UserID == "" {
		log.Debug().Msg("Fixing up login completion with the user ID from the MFA challenge")
		completed.UserID = am.mfa.UserID
	}
	log.Info().
		Str("user_locale", completed.UserSettings.Locale).
		Msg("Logged in successfully")
	return nil, &completed, nil
}
