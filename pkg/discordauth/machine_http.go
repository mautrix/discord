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
	"fmt"
	"io"
	"maps"
	"net/http"

	"github.com/rs/zerolog"
)

// decorateReq modifies the given [http.Request] according to the current
// [Personality] and accrued fingerprints and state.
func (am *AuthMachine) decorateReq(req *http.Request) error {
	// Add all personality headers to the request.
	personalityHeaders, err := am.Personality.Headers()
	if err != nil {
		return fmt.Errorf("getting personality headers: %w", err)
	}
	maps.Copy(req.Header, personalityHeaders)

	debugOptions := am.Personality.DebugOptions
	if debugOptions != "" {
		req.Header.Set(HeaderDebugOptions, debugOptions)
	}
	if am.InstallationID != "" {
		req.Header.Set(HeaderInstallationID, am.InstallationID)
	}
	if !am.Fingerprint.IsZero() {
		req.Header.Set(HeaderFingerprint, am.Fingerprint.HeaderValue())
	}
	return nil
}

// do performs an HTTP request, mutating it to contain headers from the
// [Personality] and all other relevant state.
func (am *AuthMachine) do(
	ctx context.Context,
	req *http.Request,
) (*http.Response, error) {
	log := zerolog.Ctx(ctx).With().
		Str("http_method", req.Method).
		Stringer("http_url", req.URL).
		Logger()
	ctx = log.WithContext(ctx)

	if err := am.decorateReq(req); err != nil {
		return nil, fmt.Errorf("decorating request: %w", err)
	}

	resp, err := am.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making http request: %w", err)
	}

	return resp, err
}

// A CaptchaError reports that Discord preempted a request with a CAPTCHA
// challenge. Callers that can converse with the user may recover the challenge
// and retry the request with a solution.
type CaptchaError struct {
	Captcha *Captcha
}

func (e *CaptchaError) Error() string {
	return "discord presented a captcha challenge"
}

// exchange performs a single HTTP request against Discord that is mutated to
// contain headers from the [Personality] and all other relevant state accrued
// so far.
//
// The response body is consumed in its entirety.
//
// CAPTCHA challenges are recognized and returned as [CaptchaError]s. Other
// Discord API errors are returned as [APIError]s, or [HTTPError]s when
// unrecognized.
func (am *AuthMachine) exchange(ctx context.Context, req *http.Request) ([]byte, error) {
	// TODO: Retry on transient network failures?
	resp, err := am.do(ctx, req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		log := zerolog.Ctx(ctx)
		log.Warn().Err(err).Msg("Failed to close response body, proceeding")
	}

	if cap := CheckCaptcha(ctx, resp, body); cap != nil {
		return body, &CaptchaError{Captcha: cap}
	}
	if !respIsOk(resp) {
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil || apiErr.Code == 0 {
			// We got an error but couldn't unmarshal it into an APIError;
			// perhaps some Cloudflare/load balancer thing. Return a
			// generic error.
			return body, HTTPError{body: body, resp: resp}
		}
		apiErr.ResponseBody = body
		return body, apiErr
	}
	return body, nil
}

// post constructs an [http.Request] that POSTs a JSON-marshaled body.
func (am *AuthMachine) post(
	ctx context.Context,
	endpoint string,
	jsonBody any,
) (*http.Request, error) {
	jsonBytes, err := json.Marshal(jsonBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling post body: %w", err)
	}

	url := am.APIBase + endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("constructing post request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}
