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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
)

// An AuthMachine governs the core logic to authenticate with Discord. It is
// concerned with:
//
//   - Detecting CAPTCHA challenges.
//   - Sending the correct set of headers to each endpoint.
//   - Stashing the necessary state in-memory and threading them into requests
//     as necessary.
type AuthMachine struct {
	log        *zerolog.Logger
	LogFilters AuthMachineLogFilters

	http    HTTP
	APIBase string
	handler ChallengeHandler

	State AuthMachineState

	Personality *Personality
}

type AuthMachineState struct {
	Fingerprint    Fingerprint
	InstallationID string
}

type CaptchaSolution struct {
	Solution string
}
type CaptchaHandler func(ctx context.Context, captcha *Captcha) (*CaptchaSolution, error)

func NewAuthMachine(ctx context.Context, http HTTP, personality *Personality, handler ChallengeHandler) *AuthMachine {
	if http == nil {
		panic("http interface is required")
	}
	if personality == nil {
		panic("personality is required")
	}
	if handler == nil {
		panic("handler is required")
	}

	log := zerolog.Ctx(ctx).With().Str("component", "discord auth").Logger()

	return &AuthMachine{
		log: &log,

		http:    http,
		handler: handler,

		APIBase:     "https://discord.com/api/v9",
		Personality: personality,
	}
}

func formatHTTPHeaderDump(prefix string, headers http.Header) string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	var msg strings.Builder
	msg.WriteString(prefix)
	for _, key := range keys {
		for _, value := range headers[key] {
			msg.WriteByte('\n')
			msg.WriteString(key)
			msg.WriteString(": ")
			msg.WriteString(value)
		}
	}

	return msg.String()
}

func (am *AuthMachine) captchaRetryLoop(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	// Check if we can clone the request body. We need this since we might need
	// to retry the request.
	if req.GetBody == nil && req.ContentLength > 0 {
		return nil, nil, fmt.Errorf("tried to make request with a body that isn't retriable")
	}

	log := zerolog.Ctx(ctx)
	nCaptchas := 0
	var resp *http.Response
	var err error

	defer func() {
		if resp == nil {
			return
		}

		respLogLevel := zerolog.DebugLevel
		respStatusOk := respIsOk(resp)
		if !respStatusOk {
			respLogLevel = zerolog.ErrorLevel
		}

		if am.LogFilters.EveryHTTPResponse || !respStatusOk {
			// Erroneous responses are always logged.
			log.WithLevel(respLogLevel).
				Int("n_captchas", nCaptchas).
				Int("http_status", resp.StatusCode).
				Int("http_content_length", int(resp.ContentLength)).
				Msg("Received response")
		}
	}()

	for {
		if am.LogFilters.EveryHTTPRequest {
			log.Debug().
				Int("n_captchas", nCaptchas).
				Msg("Making request")
		}
		if am.LogFilters.DangerouslyLeakyHTTPHeaders {
			log.Debug().
				Int("n_captchas", nCaptchas).
				Msg(formatHTTPHeaderDump("Sending request headers", req.Header))
		}

		// Make the HTTP request.
		resp, err = am.http.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to make http request: %w", err)
		}
		if am.LogFilters.DangerouslyLeakyHTTPHeaders {
			log.Debug().
				Int("n_captchas", nCaptchas).
				Msg(formatHTTPHeaderDump("Received response headers", resp.Header))
		}

		// We need to consume the entire response body so we can test for a
		// CAPTCHA challenge.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to slurp http response body: %w", err)
		}
		if err := resp.Body.Close(); err != nil {
			log.Warn().Err(err).Msg("Failed to close response body, proceeding")
		}

		captcha := TryUnmarshalingCaptcha(ctx, resp, body)
		if captcha != nil {
			goto solveCaptchaAndRetry
		}

		if !respIsOk(resp) {
			// (defer block above logs for us.)

			var apiError APIError
			err := json.Unmarshal(body, &apiError)

			if err != nil || apiError.Code == 0 {
				// Doesn't look like we got {"code": 00000, "message": "..."}
				return nil, nil, HTTPError{body: body, resp: resp}
			} else {
				apiError.ResponseBody = body
				return nil, nil, apiError
			}
		}

		// No CAPTCHA, we're good.
		return resp, body, nil

	solveCaptchaAndRetry:
		// We got a CAPTCHA. Invoke the handler provided by the client and
		// retry with the challenge response once the CAPTCHA is completed.

		log = ptr.Ptr(captcha.LogContext(log.With()).Logger())
		log.Info().Msg("Encountered CAPTCHA challenge")

		solution, err := am.waitForCaptchaSolve(ctx, captcha)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to wait for captcha solution: %w", err)
		}

		// We're going to try the request again once we come back around in the
		// loop.
		req, err = refreshReq(ctx, req)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to refresh request: %w", err)
		}
		// Add the solution and other CAPTCHA state to the headers.
		req.Header.Set(HeaderCaptchaKey, solution.Solution)
		captcha.UpdateHeaders(&req.Header)
	}
}

func (am *AuthMachine) waitForCaptchaSolve(ctx context.Context, captcha *Captcha) (*CaptchaSolution, error) {
	log := zerolog.Ctx(ctx).With().Str("action", "wait for discord captcha solve").Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("Invoking CAPTCHA handler")
	solution, err := am.handler.SolveCaptcha(ctx, captcha)
	if err != nil {
		return nil, fmt.Errorf("captcha handler failed: %w", err)
	}
	if solution == nil {
		return nil, fmt.Errorf("captcha handler returned nil solution")
	}

	return solution, nil
}

// doHandlingCaptcha performs an HTTP request, mutating it to contain headers
// from the [Personality].
//
//   - In order to detect and respond to CAPTCHA challenges, this method buffers
//     all request and response bodies into memory.
//
//   - Should a CAPTCHA challenge occur, note that multiple attempts to solve the
//     CAPTCHA may be necessary.
func (am *AuthMachine) doHandlingCaptcha(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	log := zerolog.Ctx(ctx).With().
		Str("http_method", req.Method).
		Stringer("http_url", req.URL).
		Logger()
	ctx = log.WithContext(ctx)

	// Add all personality headers to the request.
	personalityHeaders, err := am.Personality.Headers()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get personality headers: %w", err)
	}
	maps.Copy(req.Header, personalityHeaders)
	// Set X-Debug-Options if we have one.
	debugOptions := am.Personality.DebugOptions
	if debugOptions != "" {
		req.Header.Set(HeaderDebugOptions, debugOptions)
	}
	// Set X-Installation-ID if we have one.
	if am.State.InstallationID != "" {
		req.Header.Set(HeaderInstallationID, am.State.InstallationID)
	}
	// Set X-Fingerprint if we have one.
	if !am.State.Fingerprint.IsZero() {
		req.Header.Set(HeaderFingerprint, am.State.Fingerprint.HeaderValue())
	}

	// Make the request, anticipating any potential CAPTCHAs.
	resp, body, err := am.captchaRetryLoop(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to make request: %w", err)
	}

	return resp, body, err
}

func (am *AuthMachine) performLegacyExperiments(ctx context.Context) (*ExperimentsLegacy, error) {
	url := fmt.Sprintf("%s/experiments?with_guild_experiments=true", am.APIBase)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct legacy experiments request: %w", err)
	}

	// Set X-Context-Properties.
	contextProps, err := EncodeBasicContextProperties(ContextLocationLogin)
	if err != nil {
		return nil, fmt.Errorf("failed to encode login context properties: %w", err)
	}
	req.Header.Set(HeaderContextProperties, contextProps)

	_, body, err := am.doHandlingCaptcha(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to request legacy experiments: %w", err)
	}

	var legacy ExperimentsLegacy
	err = json.Unmarshal(body, &legacy)
	if err != nil {
		return nil, fmt.Errorf("failed to decode legacy experiments: %w", err)
	}

	return &legacy, nil
}

func (am *AuthMachine) performApexExperiments(ctx context.Context) (any, error) {
	url := fmt.Sprintf("%s/apex/experiments?surface=2", am.APIBase)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct apex experiments request: %w", err)
	}

	// (Apex experiments don't get `X-Context-Properties`.)
	_, body, err := am.doHandlingCaptcha(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to request apex experiments: %w", err)
	}

	var apexExperiments struct {
		InstallationID string `json:"installation"`
	}
	err = json.Unmarshal(body, &apexExperiments)
	if err != nil {
		return nil, fmt.Errorf("failed to decode apex experiments: %w", err)
	}

	am.State.InstallationID = apexExperiments.InstallationID

	return nil, nil
}

// Prepare loads the login page and situates the AuthMachine with an
// experiments-related [Fingerprint]. It is important for Prepare to be called
// before [AuthMachine.Login].
//
// Calling this method can lead to your [ChallengeHandler] being called.
func (am *AuthMachine) Prepare(ctx context.Context) error {
	log := am.log.With().Str("action", "prepare discord auth machine").Logger()
	ctx = log.WithContext(ctx)

	if !am.State.Fingerprint.IsZero() {
		log.Debug().Msg("Already prepared")
		return nil
	}

	log.Info().Msg("Preparing Discord auth")

	legacy, err := am.performLegacyExperiments(ctx)
	if err != nil {
		return fmt.Errorf("failed to perform legacy experiments: %w", err)
	}

	_, err = am.performApexExperiments(ctx)
	if err != nil {
		return fmt.Errorf("failed to perform apex experiments: %w", err)
	}

	// (Apex experiments aren't fetched with the fingerprint, so only set it
	// now.)
	if !legacy.Fingerprint.IsZero() {
		am.State.Fingerprint = legacy.Fingerprint
	}
	if am.LogFilters.Fingerprint {
		log.Info().Str("fingerprint", am.State.Fingerprint.HeaderValue()).Msg("Loaded Discord fingerprint")
	}

	return nil
}

// FIXME(skip): Load the HTML /login page before anything else so we can seed our cookies with Cloudflare stuff.
// FIXME(skip): Handle IP verification.
// FIXME(skip): Handle suspended user tokens.

// Once you have called [AuthMachine.Prepare], Login kicks off the login
// process and doesn't return until the login is complete and a token is
// acquired, unless an error occurs at any point.
//
// CAPTCHA and MFA handling is automatically relegated to your
// [ChallengeHandler] and its methods will be called as necessary.
func (am *AuthMachine) Login(ctx context.Context, creds *Creds) (*LoginCompleted, error) {
	log := zerolog.Ctx(ctx)

	if am.State.Fingerprint.IsZero() {
		return nil, fmt.Errorf("can't log in without a fingerprint (forgot to call Prepare?)")
	}

	firstLoginReq, err := am.POST(ctx, "/auth/login", creds)
	if err != nil {
		return nil, fmt.Errorf("failed to construct login request: %w", err)
	}

	_, body, err := am.doHandlingCaptcha(ctx, firstLoginReq)
	if err != nil {
		return nil, fmt.Errorf("failed to request login: %w", err)
	}

	loginResponse, err := am.handleFirstLoginResponse(ctx, body)
	if err != nil {
		return nil, err
	}

	if am.LogFilters.SuccessfulLogin {
		ev := log.Info()
		if am.LogFilters.LoggedInUserID {
			ev = ev.Str("user_id", loginResponse.UserID).Str("user_locale", loginResponse.UserSettings.Locale)
		}
		ev.Msg("Logged in successfully")
	}

	return loginResponse, nil
}

// handleFirstLoginResponse handles the response body from POSTing to
// /auth/login. This will either complete the login or begin an MFA flow.
func (am *AuthMachine) handleFirstLoginResponse(ctx context.Context, loginRespBody []byte) (*LoginCompleted, error) {
	log := zerolog.Ctx(ctx)

	var completed LoginCompleted
	err := json.Unmarshal(loginRespBody, &completed)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal login response: %w", err)
	}
	if !completed.HasToken() {
		log.Debug().Msg("Response lacked a token, attempting to handle as MFA")
		completedMfa, err := am.tryHandlingMFA(ctx, loginRespBody)
		if err != nil {
			return nil, fmt.Errorf("failed to handle potential MFA: %w", err)
		}

		if completedMfa == nil || !completedMfa.HasToken() {
			// Still unable to handle whatever we got as a response from POST
			// /auth/login, give up. Log the response for diagnostics.
			log.Error().Str("response_body", string(loginRespBody)).Msg("Received corrupted login response")
			return nil, fmt.Errorf("corrupted login response")
		}
		return completedMfa, nil
	}

	return &completed, nil
}

func (am *AuthMachine) requestSMSCode(ctx context.Context, state *MFAState) (*SMSSendResponse, error) {
	smsSendReq, err := am.POST(ctx, "/auth/mfa/sms/send", SMSSendRequest{
		Ticket: state.Ticket,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to construct SMS send code request: %w", err)
	}
	smsSendReq.Header.Set("Content-Type", "application/json")

	_, body, err := am.doHandlingCaptcha(ctx, smsSendReq)
	if err != nil {
		return nil, fmt.Errorf("failed to request SMS code: %w", err)
	}

	var resp SMSSendResponse
	err = json.Unmarshal(body, &resp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal SMS code response: %w", err)
	}

	return &resp, nil
}

func (am *AuthMachine) tryHandlingMFA(ctx context.Context, loginRespBody []byte) (*LoginCompleted, error) {
	baseLog := zerolog.Ctx(ctx)

	var required LoginMFARequired
	err := json.Unmarshal(loginRespBody, &required)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal mfa required: %w", err)
	}

	if !required.MFARequired {
		// This isn't actually a LoginMFARequired.
		return nil, nil
	}

	logCtx := baseLog.With().
		Str("mfa_login_instance_id", required.LoginInstanceID).
		Bool("mfa_accepting_backup_codes", required.BackupCodesAccepted).
		Bool("mfa_sms_enabled", required.SMSEnabled).
		Bool("mfa_totp_enabled", required.TOTPEnabled).
		Bool("mfa_has_webauthn_credential", required.WebAuthnCredential != nil)
	if am.LogFilters.LoggedInUserID {
		logCtx = logCtx.Str("user_id", required.UserID)
	}
	log := logCtx.Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("Need to log in with MFA")

	var prevErr error
	for {
		completed, err := am.attemptMFA(ctx, &required, prevErr)
		if err == nil {
			// Success.
			return completed, nil
		}

		var apiErr APIError
		if errors.As(err, &apiErr) && apiErr.IsUserInputError() {
			log.Warn().Err(err).Msg("MFA submission rejected, prompting user again")
			prevErr = apiErr
			continue
		}

		// Failed for some other reason, bail.
		return nil, err
	}
}

func (am *AuthMachine) attemptMFA(
	ctx context.Context,
	required *LoginMFARequired,
	prevErr error,
) (*LoginCompleted, error) {
	log := zerolog.Ctx(ctx)
	cont, err := am.handler.ContinueMFA(ctx, &MFAChallenge{
		LoginMFARequired: required,
		RequestSMS: func(ctx context.Context) (*SMSSendResponse, error) {
			// Thread the MFAState through on behalf of the client.
			return am.requestSMSCode(ctx, &required.MFAState)
		},
		PreviousError: prevErr,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to continue mfa flow: %w", err)
	}
	if cont == nil {
		return nil, fmt.Errorf("handler didn't return a MFA continuation")
	}

	log.Info().Str("mfa_type", string(cont.Type)).Msg("Continuing with MFA flow")
	contReq, err := am.POST(ctx, fmt.Sprintf("/auth/mfa/%s", cont.Type), cont.MFAContinuation)
	if err != nil {
		return nil, fmt.Errorf("failed to construct MFA continuation request: %w", err)
	}
	_, body, err := am.doHandlingCaptcha(ctx, contReq)
	if err != nil {
		return nil, fmt.Errorf("failed to complete MFA flow: %w", err)
	}

	var completed LoginCompleted
	err = json.Unmarshal(body, &completed)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal completed MFA: %w", err)
	}

	// Discord omits the user ID when completing the MFA flow as we already
	// received it as part of LoginMFARequired. Re-add it here.
	if completed.UserID == "" {
		log.Trace().Msg("Fixing up MFA completion with the user ID")
		completed.UserID = required.UserID
	}

	return &completed, nil
}

func (am *AuthMachine) POST(ctx context.Context, endpoint string, jsonBody any) (*http.Request, error) {
	jsonBytes, err := json.Marshal(jsonBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal body for request: %w", err)
	}

	url := am.APIBase + endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to make POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}
