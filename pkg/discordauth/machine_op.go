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
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
)

// pendingRequest is an intent to perform some discrete, effectful HTTP
// operation that the [AuthMachine] can attempt, derived directly from some
// user input.
//
// Fundamentally, a pendingRequest holds enough information to construct an
// HTTP request, interpret a successful response, and potentially translate API
// errors into the next [Prompt].
//
// pendingRequests are essential to letting [AuthMachine] properly suspend
// around challenges and preemptions such as CAPTCHA and IP verification flows,
// where the same request needs to be retried verbatim at some indeterminate
// point in the future.
type pendingRequest struct {
	name string

	// request builds a fresh HTTP request for this operation.
	//
	// [AuthMachine.pump] may call request more than once for the same
	// pendingRequest, such as after CAPTCHA or email verification. It must not
	// return a request whose body has already been consumed.
	request func(context.Context, *AuthMachine) (*http.Request, error)

	// succeed interprets a successful HTTP response body and advances the
	// authentication flow by returning exactly one of a non-nil [Prompt], a
	// [LoginCompleted], or an error; the other two results are nil.
	succeed func(context.Context, *AuthMachine, []byte) (*Prompt, *LoginCompleted, error)

	// fail, when non-nil, defines bespoke Discord API error handling for this
	// operation.
	//
	// Generic API errors, CAPTCHA challenges, email verification, and other
	// higher-level concerns are already handled by [AuthMachine.pump]. fail is
	// for cases where this specific pendingRequest knows that an API error
	// should become another prompt, e.g. "invalid credentials, try again".
	//
	// By returning a non-nil [Prompt], the user can be sent back to
	// a previous step.
	//
	// Any error returned from this function is propagated out of the state
	// machine.
	fail func(context.Context, *AuthMachine, APIError) (*Prompt, error)
}

func loginOp(creds *Creds) *pendingRequest {
	return &pendingRequest{
		name: "login",
		request: func(ctx context.Context, am *AuthMachine) (*http.Request, error) {
			// Prepare was not called?
			if am.Fingerprint.IsZero() {
				return nil, fmt.Errorf("cannot consume credentials without a fingerprint")
			}
			return am.POST(ctx, "/auth/login", creds)
		},
		succeed: func(ctx context.Context, am *AuthMachine, body []byte) (*Prompt, *LoginCompleted, error) {
			return am.handleAuthResponse(ctx, body)
		},
		fail: func(ctx context.Context, am *AuthMachine, err APIError) (*Prompt, error) {
			if err.IsUserInputError() {
				return &Prompt{CredsPrompt: &CredsPrompt{Reason: "Invalid email/phone number or password."}}, nil
			}
			return nil, err
		},
	}
}

func sendSMSOp(mfaState *MFAState) *pendingRequest {
	return &pendingRequest{
		name: "send_sms",
		request: func(ctx context.Context, am *AuthMachine) (*http.Request, error) {
			return am.POST(ctx, "/auth/mfa/sms/send", SMSSendRequest{
				Ticket: mfaState.Ticket,
			})
		},
		succeed: func(ctx context.Context, am *AuthMachine, body []byte) (*Prompt, *LoginCompleted, error) {
			log := zerolog.Ctx(ctx)
			log.Info().Msg("Sent MFA code to SMS")
			return &Prompt{MFACodePrompt: &MFACodePrompt{Type: AuthenticatorSMS}}, nil, nil
		},
	}
}

func continueMFAOp(cont *MFAContinue, challenge *LoginMFARequired) *pendingRequest {
	return &pendingRequest{
		name: "continue_mfa",
		request: func(ctx context.Context, am *AuthMachine) (*http.Request, error) {
			return am.POST(ctx, fmt.Sprintf("/auth/mfa/%s", cont.Type), cont.MFAContinuation)
		},
		succeed: func(ctx context.Context, am *AuthMachine, body []byte) (*Prompt, *LoginCompleted, error) {
			// /auth/mfa/<type>, upon success, responds with a body similar to
			// that returned by /auth/login.
			prompt, completed, err := am.handleAuthResponse(ctx, body)
			if err != nil {
				return nil, nil, err
			}
			return prompt, completed, nil
		},
		fail: func(ctx context.Context, am *AuthMachine, err APIError) (*Prompt, error) {
			if err.IsUserInputError() {
				return &Prompt{MFAChallengePrompt: &MFAChallengePrompt{
					LoginMFARequired: challenge,
					Reason:           err.Error(),
				}}, nil
			}
			return nil, err
		},
	}
}
