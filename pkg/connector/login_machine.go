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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/util/l10n"
	"golang.org/x/net/publicsuffix"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordauth"
	"go.mau.fi/mautrix-discord/pkg/discordtransport"
)

var ErrMissingLoginInput = bridgev2.RespError{
	ErrCode:    "FI.MAU.DISCORD.MISSING_LOGIN_INPUT",
	Err:        "Missing required login input",
	StatusCode: http.StatusBadRequest,
}

var ErrClientHTTPFailed = bridgev2.RespError{
	ErrCode:    "FI.MAU.DISCORD.CLIENT_HTTP_FAILED",
	Err:        "The request did not complete on this device. Check your internet connection, VPN or content filter and try again.",
	StatusCode: http.StatusBadRequest,
}

func isClientHTTPError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "error from client: ")
}

func userVisibleLoginError(ctx context.Context, err error) error {
	if isClientHTTPError(err) {
		zerolog.Ctx(ctx).Err(err).Msg("Discord login request failed on the client")
		return ErrClientHTTPFailed.WithInternalError(err)
	}

	var apiErr discordauth.APIError
	if !errors.As(err, &apiErr) {
		return userVisibleRESTError(ctx, err)
	}

	zerolog.Ctx(ctx).Err(apiErr).
		Int("discord_error_code", int(apiErr.Code)).
		Msg("Propagating Discord error to user")

	if apiErr.AnyFieldHasError(discordauth.AccountCompromisedResetPassword) {
		return bridgev2.RespError{
			ErrCode:    "FI.MAU.DISCORD.ACCOUNT_COMPROMISED",
			Err:        "Discord has flagged this account as compromised and requires a password reset. Reset your password on Discord, then try logging in again.",
			StatusCode: http.StatusForbidden,
		}
	}

	// Directly surface the root error message, falling back to the debug
	// representation.
	msg := apiErr.Message
	if msg == "" {
		msg = apiErr.Error()
	}

	return bridgev2.RespError{
		ErrCode:    fmt.Sprintf("FI.MAU.DISCORD.API_%d", apiErr.Code),
		Err:        msg,
		StatusCode: http.StatusBadRequest,
	}
}

// userVisibleRESTError handles plain discordgo REST failures, which is what the token,
// browser and QR flows produce when Discord rejects a token outright.
func userVisibleRESTError(ctx context.Context, err error) error {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Response == nil {
		return err
	}

	logEvt := zerolog.Ctx(ctx).Err(err).Int("http_status", restErr.Response.StatusCode)
	if restErr.Message != nil {
		logEvt = logEvt.Int("discord_error_code", restErr.Message.Code)
	}
	logEvt.Msg("Propagating Discord REST error to user")

	switch restErr.Response.StatusCode {
	case http.StatusUnauthorized:
		return bridgev2.RespError{
			ErrCode:    "FI.MAU.DISCORD.INVALID_TOKEN",
			Err:        "Discord rejected that token. Please log in again to get a fresh one.",
			StatusCode: http.StatusUnauthorized,
		}
	case http.StatusForbidden:
		return bridgev2.RespError{
			ErrCode:    "FI.MAU.DISCORD.FORBIDDEN",
			Err:        "Discord refused access to this account. Check the account on Discord, then try again.",
			StatusCode: http.StatusForbidden,
		}
	case http.StatusTooManyRequests:
		return bridgev2.RespError{
			ErrCode:    "FI.MAU.DISCORD.RATE_LIMITED",
			Err:        "Discord is rate limiting the sign-in. Please wait a few minutes and try again.",
			StatusCode: http.StatusBadRequest,
		}
	}

	// Discord's own message is written for end users, so prefer it when present.
	if restErr.Message != nil && restErr.Message.Message != "" {
		return bridgev2.RespError{
			ErrCode:    fmt.Sprintf("FI.MAU.DISCORD.API_%d", restErr.Message.Code),
			Err:        restErr.Message.Message,
			StatusCode: http.StatusBadRequest,
		}
	}
	return err
}

const LoginFlowIDMachine = "machine"
const LoginStepIDMachineInitialCreds = "fi.mau.discord.creds"
const LoginStepIDMachineCaptcha = "fi.mau.discord.captcha"
const LoginStepIDMachineEmailVerification = "fi.mau.discord.email_verification"
const LoginStepIDMachineSMSVerification = "fi.mau.discord.sms_verification"
const LoginStepIDMachineMFAMethod = "fi.mau.discord.mfa.method"
const LoginStepIDMachineMFATOTP = "fi.mau.discord.mfa.totp"
const LoginStepIDMachineMFABackup = "fi.mau.discord.mfa.backup"
const LoginStepIDMachineMFASMS = "fi.mau.discord.mfa.sms"
const InputDataFieldIDUsernameOrPhone = "username_or_phone"
const InputDataFieldIDPassword = "password"
const InputDataFieldIDMFAMethod = "mfa_method"
const InputDataFieldIDMFABackupCode = "mfa_backup_code"
const InputDataFieldIDMFASMSCode = "mfa_sms_code"
const InputDataFieldIDMFATOTPCode = "mfa_totp_code"
const InputDataFieldIDEmailVerification = "email_verification"
const InputDataFieldIDSMSCode = "sms_code" // IP verification via SMS code

type mfaOption string

const (
	mfaSms    mfaOption = "Text me a code"
	mfaTotp   mfaOption = "Use my authenticator app"
	mfaBackup mfaOption = "Enter a backup code"
)

type DiscordMachineLogin struct {
	*DiscordGenericLogin
	Machine *discordauth.AuthMachine

	personality  discordauth.Personality
	mfaChallenge *discordauth.LoginMFARequired
}

var _ bridgev2.LoginProcessWithParams = (*DiscordMachineLogin)(nil)
var _ bridgev2.LoginProcessUserInput = (*DiscordMachineLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*DiscordMachineLogin)(nil)

func NewDiscordMachineLogin(ctx context.Context, login *DiscordGenericLogin) (*DiscordMachineLogin, error) {
	launchSig, err := discordgo.NewVanillaSignature()
	if err != nil {
		return nil, fmt.Errorf("failed to generate launch signature: %w", err)
	}

	personality := discordauth.Personality{
		UserAgent:    discordgo.DroidBrowserUserAgent,
		Locale:       "en-US",
		TimeZone:     "UTC",
		DebugOptions: discordauth.DefaultDebugOptions,
		// TODO dedupe with droid.go in discordgo
		SuperProperties: discordauth.SuperProperties{
			OS:                "Windows",
			Browser:           "Chrome",
			SystemLocale:      "en-US",
			HasClientMods:     false,
			BrowserUserAgent:  discordgo.DroidBrowserUserAgent,
			BrowserVersion:    discordgo.DroidBrowserVersion,
			OSVersion:         "10",
			ReleaseChannel:    "stable",
			ClientBuildNumber: discordgo.DroidClientBuildNumber(),
			ClientLaunchID:    uuid.NewString(),
			LaunchSignature:   launchSig,
			ClientAppState:    "focused",
		},
		// TODO(skip): These are different for different kinds of requests.
		ExtraHeaders: map[string]string{
			"sec-ch-ua":          discordgo.DroidBaseHeaders["Sec-Ch-Ua"],
			"sec-ch-ua-mobile":   "?0",
			"sec-ch-ua-platform": discordgo.DroidBaseHeaders["Sec-Ch-Ua-Platform"],
			"sec-fetch-dest":     "empty",
			"sec-fetch-mode":     "cors",
			"sec-fetch-site":     "same-origin",
		},
	}

	return &DiscordMachineLogin{
		DiscordGenericLogin: login,
		personality:         personality,
	}, nil
}

func (d *DiscordMachineLogin) httpClient(ctx context.Context, clientHTTP bridgev2.FingerprintingRoundTripper) (*http.Client, error) {
	if clientHTTP != nil {
		clientHTTP.SetFingerprint("chrome")
		jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		if err != nil {
			return nil, fmt.Errorf("failed to create cookie jar: %w", err)
		}
		return &http.Client{Transport: clientHTTP, Jar: jar}, nil
	}

	// Resolve the HTTP client settings (proxy) for use during login.
	// NOTE(skip): This is grossly tangled. Think of a way to restructure this.
	var settings exhttp.ClientSettings
	if d.connector.Config.ProxyLoginMachine {
		var err error
		settings, err = d.connector.resolveHTTPClientSettings(ctx, "login")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve proxy: %w", err)
		}
	} else {
		settings = d.connector.Bridge.GetHTTPClientSettings()
	}
	return discordtransport.CompileTransport(settings, discordtransport.TransportOptions{
		CookieJar: true,
	})
}

func (d *DiscordMachineLogin) Cancel() {
	d.DiscordGenericLogin.Cancel()
}

// initialCredsStep returns the very first login step needed to kick off the
// authentication flow (email or phone number and password).
func initialCredsStep(instructions l10n.Message) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineInitialCreds,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   InputDataFieldIDUsernameOrPhone,
					Name: msgFieldUsernameOrPhoneName.Message(),
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   InputDataFieldIDPassword,
					Name: msgFieldPasswordName.Message(),
				},
			},
		},
	}
}

func emailVerificationStep(retrying bool) *bridgev2.LoginStep {
	// Forcing a dummy input like this isn't ideal by any means, but
	// chat-command login and Beeper iOS cannot handle a user_input step with
	// no inputs.
	instructions := msgStepEmailVerificationInstructions.Message()
	if retrying {
		instructions = msgStepEmailVerificationRetryInstructions.Message()
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineEmailVerification,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeSelect,
					ID:   InputDataFieldIDEmailVerification,
					Name: msgFieldEmailVerificationName.Message(),
					Options: []bridgev2.LoginInputOption{
						{Value: "I’ve verified the login", Label: msgFieldEmailVerificationOptVerified.Message()},
					},
				},
			},
		},
	}
}

type smsCodeStepOptions struct {
	loginStepID  string
	inputFieldID string
	instructions l10n.Message // optional, defaults to [msgStepMFASMSInstructions]
}

func smsCodeStep(opts smsCodeStepOptions) *bridgev2.LoginStep {
	if opts.instructions.IsZero() {
		opts.instructions = msgStepMFASMSInstructions.Message()
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       opts.loginStepID,
		Instructions: opts.instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Description: msgFieldSMSCodeDescription.Message(),
					ID:          opts.inputFieldID,
					Name:        msgFieldSMSCodeName.Message(),
					// TODO enforce length
					Pattern: `^(\d+)$`,
					Type:    bridgev2.LoginInputFieldType2FACode,
				},
			},
		},
	}
}

func (d *DiscordMachineLogin) mfaMethodStep(ctx context.Context, prompt *discordauth.MFAChallengePrompt) (*bridgev2.LoginStep, error) {
	challenge := prompt.LoginMFARequired
	if challenge == nil {
		return nil, fmt.Errorf("auth machine returned an MFA prompt without a challenge")
	}

	log := zerolog.Ctx(ctx).With().
		Str("action", "discord machine continue mfa").
		Str("login_instance_id", challenge.LoginInstanceID).
		Bool("mfa_required", challenge.MFARequired).
		Bool("mfa_sms_enabled", challenge.SMSEnabled).
		Bool("mfa_totp_enabled", challenge.TOTPEnabled).
		Bool("mfa_backup_codes_accepted", challenge.BackupCodesAccepted).
		Logger()
	log.Info().Msg("Entering MFA login flow")

	mfaOptions := make([]bridgev2.LoginInputOption, 0, 3)
	if challenge.SMSEnabled {
		mfaOptions = append(mfaOptions, bridgev2.LoginInputOption{Value: string(mfaSms), Label: msgFieldMFAMethodOptSMS.Message()})
	}
	if challenge.TOTPEnabled {
		mfaOptions = append(mfaOptions, bridgev2.LoginInputOption{Value: string(mfaTotp), Label: msgFieldMFAMethodOptTOTP.Message()})
	}
	if challenge.BackupCodesAccepted {
		mfaOptions = append(mfaOptions, bridgev2.LoginInputOption{Value: string(mfaBackup), Label: msgFieldMFAMethodOptBackup.Message()})
	}
	if len(mfaOptions) == 0 {
		return nil, fmt.Errorf("no supported MFA methods available (WebAuthn is unimplemented)")
	}

	instructions := msgStepMFAMethodInstructions.Message()
	if prompt.Reason != "" {
		if challenge.BackupCodesAccepted {
			instructions = msgStepMFAMethodRetryBackupInstructions.Message()
		} else {
			instructions = msgStepMFAMethodRetryInstructions.Message()
		}
	}

	d.mfaChallenge = challenge
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineMFAMethod,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:    bridgev2.LoginInputFieldTypeSelect,
					ID:      InputDataFieldIDMFAMethod,
					Name:    msgFieldMFAMethodName.Message(),
					Options: mfaOptions,
				},
			},
		},
	}, nil
}

func mfaCodeStep(authType discordauth.AuthenticatorType) (*bridgev2.LoginStep, error) {
	switch authType {
	case discordauth.AuthenticatorBackup:
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       LoginStepIDMachineMFABackup,
			Instructions: msgStepMFABackupInstructions.Message(),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:        bridgev2.LoginInputFieldTypePassword,
						ID:          InputDataFieldIDMFABackupCode,
						Name:        msgFieldMFABackupCodeName.Message(),
						Description: msgFieldMFABackupCodeDescription.Message(),
					},
				},
			},
		}, nil
	case discordauth.AuthenticatorTOTP:
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       LoginStepIDMachineMFATOTP,
			Instructions: msgStepMFATOTPInstructions.Message(),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type: bridgev2.LoginInputFieldType2FACode,
						ID:   InputDataFieldIDMFATOTPCode,
						Name: msgFieldMFATOTPCodeName.Message(),
						// TODO enforce length
						Pattern: `^(\d+)$`,
					},
				},
			},
		}, nil
	case discordauth.AuthenticatorSMS:
		return smsCodeStep(smsCodeStepOptions{
			loginStepID:  LoginStepIDMachineMFASMS,
			inputFieldID: InputDataFieldIDMFASMSCode,
		}), nil
	default:
		return nil, fmt.Errorf("unknown mfa authenticator type %q", authType)
	}
}

type ExtractionConfig struct {
	SiteKey   string `json:"siteKey"`
	Invisible bool   `json:"invisible"`
	RqData    string `json:"rqdata,omitempty"`
}

const CaptchaExtractionField = "captcha_token"

// The CAPTCHA must be rendered on a discord.com origin for hCaptcha to accept
// the sitekey. The exact Discord URL is mostly irrelevant, but it would be
// nice to avoid loading the actual SPA.
const captchaPageURL = "https://discord.com/company-information"

const captchaExtractionJSTemplate = `new Promise((res0, rej0) => {
  if (window.__meow_captchaPromise) {
    window.__meow_captchaPromise.then(res0, rej0)
    return
  }

  const CFG = %__CONFIG_REPLACEME__%
  window.__meow_captchaPromise = new Promise((resolve, reject) => {
    window.__meow_h = () => {
      const c = document.createElement('div')
      c.style.cssText = 'position:fixed;inset:0;z-index:2147483646;' +
        'background:#fff;display:flex;align-items:center;' +
        'justify-content:center;padding:2rem'
      document.body.append(c)

      const id = hcaptcha.render(c, {
        sitekey: CFG.siteKey,
        size: CFG.invisible ? 'invisible' : 'normal',
        callback: (token) => resolve({ captcha_token: token }),
        'error-callback': (e) => reject(new Error('hcaptcha: ' + e)),
        'expired-callback': () => reject(new Error('hcaptcha token expired')),
        'chalexpired-callback': () => reject(new Error('hcaptcha challenge expired')),
      })

      if (CFG.rqdata) {
        hcaptcha.setData(id, {rqdata: CFG.rqdata})
      }
      if (CFG.invisible) {
        hcaptcha.execute(id)
      }
    }

    const s = document.createElement('script')
    s.src = 'https://js.hcaptcha.com/1/api.js?render=explicit&onload=__meow_h&recaptchacompat=off'
    s.onerror = () => reject(new Error('failed to load hcaptcha'))
    document.head.append(s)
  })

  window.__meow_captchaPromise.then(res0, rej0)
})`

func captchaExtractionJS(cap *discordauth.Captcha) (string, error) {
	cfg := ExtractionConfig{
		Invisible: cap.Invisible,
	}
	if cap.SiteKey != nil {
		cfg.SiteKey = *cap.SiteKey
	}
	if cap.RqData != nil {
		cfg.RqData = *cap.RqData
	}

	stateJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal extraction state: %w", err)
	}

	return strings.Replace(captchaExtractionJSTemplate, "%__CONFIG_REPLACEME__%", string(stateJSON), 1), nil
}

func captchaStep(ctx context.Context, cap *discordauth.Captcha) (*bridgev2.LoginStep, error) {
	log := cap.LogContext(zerolog.Ctx(ctx).With()).Logger()

	log.Info().Msg("Encountered CAPTCHA challenge")

	if cap.Service != discordauth.CaptchaServiceHCaptcha {
		return nil, fmt.Errorf("%s captchas are currently unsupported", cap.Service)
	}

	extractJS, err := captchaExtractionJS(cap)
	if err != nil {
		return nil, fmt.Errorf("failed to compute captcha extraction JS: %w", err)
	}
	log.Debug().Str("captcha_js", extractJS).Msg("Computed CAPTCHA solution extraction JS")

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       LoginStepIDMachineCaptcha,
		Instructions: msgStepCaptchaInstructions.Message(),
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       captchaPageURL,
			ExtractJS: extractJS,
			Fields: []bridgev2.LoginCookieField{{
				ID:       CaptchaExtractionField,
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: CaptchaExtractionField,
				}},
			}},
		},
	}, nil
}

func (d *DiscordMachineLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return d.StartWithParams(ctx, bridgev2.LoginStartParams{})
}

func (d *DiscordMachineLogin) StartWithParams(ctx context.Context, params bridgev2.LoginStartParams) (*bridgev2.LoginStep, error) {
	zerolog.Ctx(ctx).Debug().Bool("client_http", params.HTTP != nil).Msg("Starting Discord machine login")
	httpClient, err := d.httpClient(ctx, params.HTTP)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	d.Machine = discordauth.NewAuthMachine(ctx, httpClient, &d.personality)

	if err := d.Machine.Prepare(ctx); err != nil {
		if isClientHTTPError(err) {
			return nil, userVisibleLoginError(ctx, err)
		}
		zerolog.Ctx(ctx).Err(err).Msg("Failed to prepare Discord login")
		return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("Failed to prepare Discord login")).WithInternalError(err)
	}

	prompt, done, err := d.Machine.Advance(ctx, nil)
	if err != nil {
		return nil, userVisibleLoginError(ctx, fmt.Errorf("failed to start login: %w", err))
	}
	if done != nil {
		return d.finalize(ctx, done)
	}
	return d.stepForPrompt(ctx, prompt)
}

func (d *DiscordMachineLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	solutionToken := cookies[CaptchaExtractionField]
	if solutionToken == "" {
		return nil, ErrMissingLoginInput.AppendMessage(": captcha solution")
	}

	return d.answer(ctx, &discordauth.Answer{
		Solution: &discordauth.CaptchaSolution{
			Solution: solutionToken,
		},
	})
}

func (d *DiscordMachineLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if _, ok := input[InputDataFieldIDUsernameOrPhone]; ok {
		return d.submitCreds(ctx, input)
	}
	if _, ok := input[InputDataFieldIDPassword]; ok {
		return d.submitCreds(ctx, input)
	}

	if code, ok := input[InputDataFieldIDSMSCode]; ok {
		return d.answer(ctx, &discordauth.Answer{SMSCode: strings.TrimSpace(code)})
	}
	if _, ok := input[InputDataFieldIDEmailVerification]; ok {
		return d.answer(ctx, &discordauth.Answer{})
	}

	if selected, ok := input[InputDataFieldIDMFAMethod]; ok {
		authType, err := mfaOptionToAuthenticator(selected)
		if err != nil {
			return nil, err
		}
		return d.answer(ctx, &discordauth.Answer{
			PickedMFAType: &authType,
		})
	}
	if hasAnyInput(input, InputDataFieldIDMFABackupCode, InputDataFieldIDMFATOTPCode, InputDataFieldIDMFASMSCode) {
		cont, err := d.mfaContinueFromInput(input)
		if err != nil {
			return nil, err
		}
		return d.answer(ctx, &discordauth.Answer{
			MFAContinue: cont,
		})
	}

	return nil, fmt.Errorf("unrecognized machine login input")
}

func (d *DiscordMachineLogin) submitCreds(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	username := strings.TrimSpace(input[InputDataFieldIDUsernameOrPhone])
	password := discordauth.NewSensitive(input[InputDataFieldIDPassword])
	if username == "" {
		return nil, ErrMissingLoginInput.AppendMessage(": username")
	}
	if password.IsZero() {
		return nil, ErrMissingLoginInput.AppendMessage(": password")
	}

	return d.answer(ctx, &discordauth.Answer{
		Creds: &discordauth.Creds{
			Login:    username,
			Password: password,
		},
	})
}

func (d *DiscordMachineLogin) answer(ctx context.Context, answer *discordauth.Answer) (*bridgev2.LoginStep, error) {
	prompt, done, err := d.Machine.Advance(ctx, answer)
	if err != nil {
		return nil, userVisibleLoginError(ctx, err)
	}
	if done != nil {
		zerolog.Ctx(ctx).Info().
			Any("required_actions", done.RequiredActions).
			Msg("Login finished")
		return d.finalize(ctx, done)
	}
	return d.stepForPrompt(ctx, prompt)
}

func (d *DiscordMachineLogin) stepForPrompt(ctx context.Context, prompt *discordauth.Prompt) (*bridgev2.LoginStep, error) {
	if prompt == nil {
		return nil, fmt.Errorf("auth machine did not advance")
	}
	log := zerolog.Ctx(ctx)

	switch {
	case prompt.CredsPrompt != nil:
		var instructions l10n.Message
		if prompt.CredsPrompt.Reason != "" {
			instructions = msgStepCredsInvalidInstructions.Message()
		}
		return initialCredsStep(instructions), nil
	case prompt.EmailVerifyPrompt != nil:
		log.Info().
			Bool("retrying", prompt.EmailVerifyPrompt.Retrying).
			Msg("Prompting user to verify the IP address via email")
		return emailVerificationStep(prompt.EmailVerifyPrompt.Retrying), nil
	case prompt.PhoneVerifyPrompt != nil:
		log.Info().Msg("Prompting user to verify the IP address via SMS")
		instructions := msgStepSMSVerificationInstructions.Message()
		if prompt.PhoneVerifyPrompt.Retrying {
			instructions = msgStepSMSVerificationRetryInstructions.Message()
		}
		return smsCodeStep(smsCodeStepOptions{
			loginStepID:  LoginStepIDMachineSMSVerification,
			inputFieldID: InputDataFieldIDSMSCode,
			instructions: instructions,
		}), nil
	case prompt.Captcha != nil:
		return captchaStep(ctx, prompt.Captcha)
	case prompt.MFAChallengePrompt != nil:
		return d.mfaMethodStep(ctx, prompt.MFAChallengePrompt)
	case prompt.MFACodePrompt != nil:
		return mfaCodeStep(prompt.MFACodePrompt.Type)
	default:
		return nil, fmt.Errorf("auth machine returned an empty prompt")
	}
}

func hasAnyInput(input map[string]string, fields ...string) bool {
	for _, field := range fields {
		if _, ok := input[field]; ok {
			return true
		}
	}
	return false
}

func mfaOptionToAuthenticator(selected string) (discordauth.AuthenticatorType, error) {
	switch mfaOption(strings.TrimSpace(selected)) {
	case mfaBackup:
		return discordauth.AuthenticatorBackup, nil
	case mfaTotp:
		return discordauth.AuthenticatorTOTP, nil
	case mfaSms:
		return discordauth.AuthenticatorSMS, nil
	default:
		return "", fmt.Errorf("unknown mfa method %q", selected)
	}
}

func (d *DiscordMachineLogin) mfaContinueFromInput(input map[string]string) (*discordauth.MFAContinue, error) {
	if d.mfaChallenge == nil {
		return nil, fmt.Errorf("no MFA challenge is active")
	}

	authType, code, ok := mfaCodeFromInput(input)
	if !ok {
		return nil, ErrMissingLoginInput.AppendMessage(": two-factor code")
	}
	if authType == discordauth.AuthenticatorBackup {
		// Discord presents MFA backup codes to the user with dashes, but the
		// backend doesn't actually accept them. Follow in the footsteps of the
		// first party clients and remove them from the user input.
		code = strings.ReplaceAll(code, "-", "")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrMissingLoginInput.AppendMessage(": two-factor code")
	}

	return &discordauth.MFAContinue{
		Type: authType,
		MFAContinuation: discordauth.MFAContinuation{
			MFAState: d.mfaChallenge.MFAState,
			Code:     code,
		},
	}, nil
}

func mfaCodeFromInput(input map[string]string) (discordauth.AuthenticatorType, string, bool) {
	if code, ok := input[InputDataFieldIDMFABackupCode]; ok {
		return discordauth.AuthenticatorBackup, code, true
	}
	if code, ok := input[InputDataFieldIDMFATOTPCode]; ok {
		return discordauth.AuthenticatorTOTP, code, true
	}
	if code, ok := input[InputDataFieldIDMFASMSCode]; ok {
		return discordauth.AuthenticatorSMS, code, true
	}
	return "", "", false
}

func (d *DiscordMachineLogin) finalize(ctx context.Context, done *discordauth.LoginCompleted) (*bridgev2.LoginStep, error) {
	ul, err := d.FinalizeCreatingLogin(ctx, done.Token.UnwrapSensitive())
	if err != nil {
		return nil, fmt.Errorf("couldn't log in via machine: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeComplete,
		StepID: LoginStepIDComplete,
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
