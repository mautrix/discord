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
	"net/http"

	"github.com/rs/zerolog"
)

const HeaderCaptchaKey = "x-captcha-key"
const HeaderCaptchaSessionID = "x-captcha-session-id"
const HeaderCaptchaRqToken = "x-captcha-rqtoken"

type CaptchaService string

const (
	CaptchaServiceHCaptcha            CaptchaService = "hcaptcha"
	CaptchaServiceReCaptcha           CaptchaService = "recaptcha"
	CaptchaServiceReCaptchaEnterprise CaptchaService = "recaptcha_enterprise"
)

// HCaptcha holds the information specific to hCaptcha within a [Captcha]
// challenge. This is only separated for organizational purposes.
type HCaptcha struct {
	SiteKey   *string `json:"captcha_sitekey"`
	SessionID *string `json:"captcha_session_id"` // re-sent in `x-captcha-session-id`
	RqData    *string `json:"captcha_rqdata"`
	RqToken   *string `json:"captcha_rqtoken"` // re-sent in `x-captcha-rqtoken`
}

func (hc *HCaptcha) SpotCheck() bool {
	return hc.SiteKey != nil && *hc.SiteKey != "" &&
		hc.SessionID != nil && *hc.SessionID != "" &&
		hc.RqData != nil && *hc.RqData != "" &&
		hc.RqToken != nil && *hc.RqToken != ""
}

func (hc *HCaptcha) UpdateHeaders(header *http.Header) {
	header.Del(HeaderCaptchaSessionID)
	header.Del(HeaderCaptchaRqToken)

	if hc.SessionID != nil {
		header.Set(HeaderCaptchaSessionID, *hc.SessionID)
	}
	if hc.RqToken != nil {
		header.Set(HeaderCaptchaRqToken, *hc.RqToken)
	}
}

// A CAPTCHA challenge from Discord.
//
// This may be returned from any endpoint at any time. To test for the presence
// of a captcha challenge, test the following criteria:
//
//  1. The HTTP status of the response is 400.
//
//  2. The captcha_key field is present on the root object of the response body
//     when parsed as JSON.
type Captcha struct {
	HCaptcha
	Key       []string       `json:"captcha_key"`
	Service   CaptchaService `json:"captcha_service"`
	Invisible bool           `json:"should_serve_invisible"`
	UserFlow  *string        `json:"user_flow"` // Unknown.
}

func (c *Captcha) LogContext(ctx zerolog.Context) zerolog.Context {
	return ctx.
		Str("captcha_service", string(c.Service)).
		Strs("captcha_key", c.Key).
		Bool("captcha_invisible", c.Invisible)
}

// CheckCaptcha tries to detect a CAPTCHA challenge in an HTTP response from
// Discord, returning a [Captcha] when one is found.
func CheckCaptcha(ctx context.Context, resp *http.Response, body []byte) *Captcha {
	if resp.StatusCode != 400 {
		return nil
	}

	log := zerolog.Ctx(ctx)

	var challenge Captcha

	err := json.Unmarshal(body, &challenge)
	if err != nil {
		// We should only hit this if the JSON is malformed or something, which
		// is probably worth knowing about.
		log.Warn().Err(err).Msg("Failed to unmarshal potential captcha challenge")
		return nil
	}

	if len(challenge.Key) > 0 {
		return &challenge
	}

	return nil
}
