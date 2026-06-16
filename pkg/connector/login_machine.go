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
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordauth"
	"go.mau.fi/mautrix-discord/pkg/discordtransport"
)

const LoginFlowIDMachine = "machine"
const LoginStepIDMachineInitialCreds = "fi.mau.discord.creds"
const LoginStepIDMachineWait = "fi.mau.discord.wait"
const LoginStepIDMachineCaptcha = "fi.mau.discord.captcha"
const LoginStepIDMachineEmailVerification = "fi.mau.discord.email_verification"
const LoginStepIDMachineMFAMethod = "fi.mau.discord.mfa.method"
const LoginStepIDMachineMFATOTP = "fi.mau.discord.mfa.totp"
const LoginStepIDMachineMFABackup = "fi.mau.discord.mfa.backup"
const LoginStepIDMachineMFASMS = "fi.mau.discord.mfa.sms"
const InputDataFieldIDUsernameOrPhone = "username_or_phone"
const InputDataFieldIDPassword = "password"
const InputDataFieldIDMFAMethod = "mfa_method"
const InputDataFieldIDMFABackupCode = "backup_code"
const InputDataFieldIDMFASMSCode = "sms_code"
const InputDataFieldIDMFATOTPCode = "totp_code"
const InputDataFieldIDEmailVerification = "email_verification"

type mfaOption string

const (
	mfaSms    mfaOption = "Text me a code"
	mfaTotp   mfaOption = "Use my authenticator app"
	mfaBackup mfaOption = "Enter a backup code"
)

// For simplicity, AuthMachine exposes a blocking, "straight-line" API:
// Prepare/Login do not yield intermediate preemption flows. Instead, they
// synchronously call back into our ChallengeHandler methods (e.g. ContinueMFA
// or SolveCaptcha) whenever user input is needed. CAPTCHA handling makes this
// especially awkward, as any request in the flow may be preempted by one or
// more CAPTCHA challenges before the original request can complete. This is
// documented in further detail in the discordauth package.
//
// Anyhow, bridgev2 is the opposite shape: login is step-based and
// request-scoped, and each provisioning request must return a LoginStep before
// its context is canceled. To bridge that mismatch, AuthMachine runs on a
// long-lived background goroutine. That worker emits signals such as "prompt
// the user", "login complete", or "login failed", and DiscordMachineLogin
// translates them into bridgev2 steps. User replies are then forwarded back to
// the worker so the synchronous AuthMachine flow can continue. Channels are
// used to bridge the gap.
//
// In practice, this means returning a dummy DisplayAndWait step to hand
// control back to bridgev2 as our Wait method drains the next signal.
// CAPTCHA challenges reuse this plumbing via LoginStepTypeCookies, dispatching
// through SubmitCookies.

type DiscordMachineLogin struct {
	*DiscordGenericLogin
	Machine *discordauth.AuthMachine

	machineCtx    context.Context
	cancelMachine context.CancelFunc

	currentlyPending   *pendingPrompt
	currentlyPendingMu sync.Mutex

	signals chan machineSignal
}

type machineSignal struct {
	prompt *pendingPrompt
	done   *discordauth.LoginCompleted
	err    error
}
type pendingPrompt struct {
	step  *bridgev2.LoginStep
	reply chan map[string]string
}

var _ discordauth.ChallengeHandler = (*DiscordMachineLogin)(nil)
var _ bridgev2.LoginProcessUserInput = (*DiscordMachineLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*DiscordMachineLogin)(nil)
var _ bridgev2.LoginProcessDisplayAndWait = (*DiscordMachineLogin)(nil)

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
			ClientBuildNumber: 497254,
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

	// Resolve the HTTP client settings (proxy) for use during login.
	// NOTE(skip): This is grossly tangled. Think of a way to restructure this.
	var settings exhttp.ClientSettings
	if login.connector.Config.ProxyLoginMachine {
		settings, err = login.connector.resolveHTTPClientSettings(ctx, "login")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve proxy: %w", err)
		}
	} else {
		settings = login.connector.Bridge.GetHTTPClientSettings()
	}

	http, err := discordtransport.CompileTransport(settings, discordtransport.TransportOptions{
		CookieJar: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	ml := &DiscordMachineLogin{
		DiscordGenericLogin: login,
	}
	ml.Machine = discordauth.NewAuthMachine(ctx, http, &personality, ml)
	return ml, nil
}

func (d *DiscordMachineLogin) WaitForEmailVerification(ctx context.Context) error {
	log := zerolog.Ctx(ctx).With().
		Str("action", "discord machine wait for email verification").
		Logger()
	ctx = log.WithContext(ctx)
	log.Info().Msg("Prompting user to verify the IP address via email")

	// This isn't ideal by any means, but chat-command login and Beeper iOS
	// cannot handle a user_input step with no inputs.
	instructions := "Your login was correct, but Discord detected Beeper " +
		"as a new login location. Check your email for a verification link, " +
		"then choose the option below to continue."
	_, err := d.promptUser(ctx, &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineEmailVerification,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeSelect,
					ID:   InputDataFieldIDEmailVerification,
					Name: "Verification",
					Options: []string{
						"I’ve verified the login",
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to wait for email verification: %w", err)
	}

	return nil
}

func (d *DiscordMachineLogin) ContinueMFA(
	ctx context.Context,
	challenge *discordauth.MFAChallenge,
) (*discordauth.MFAContinue, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "discord machine continue mfa").
		Str("login_instance_id", challenge.LoginInstanceID).
		Bool("mfa_required", challenge.MFARequired).
		Bool("mfa_sms_enabled", challenge.SMSEnabled).
		Bool("mfa_totp_enabled", challenge.TOTPEnabled).
		Bool("mfa_backup_codes_accepted", challenge.BackupCodesAccepted).
		Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("Entering MFA login flow")

	mfaOptions := make([]string, 0)
	// (Reusing the identifier strings for each authenticator method from
	// discordauth as the option enumeration values for the user prompt.)
	if challenge.SMSEnabled {
		mfaOptions = append(mfaOptions, string(mfaSms))
	}
	if challenge.TOTPEnabled {
		mfaOptions = append(mfaOptions, string(mfaTotp))
	}
	if challenge.BackupCodesAccepted {
		mfaOptions = append(mfaOptions, string(mfaBackup))
	}

	if len(mfaOptions) == 0 {
		return nil, fmt.Errorf("no supported MFA methods available (WebAuthn is unimplemented)")
	}

	instructions := "How do you want to verify it’s you?"
	if challenge.PreviousError != nil {
		instructions = "That code didn’t work. Choose how you’d like to verify and try again."
		if challenge.BackupCodesAccepted {
			instructions += " If your authenticator app isn’t working, you can use a backup code instead."
		}
	}

	input, err := d.promptUser(ctx, &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineMFAMethod,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:    bridgev2.LoginInputFieldTypeSelect,
					ID:      InputDataFieldIDMFAMethod,
					Name:    "Verification Method",
					Options: mfaOptions,
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prompt for MFA method: %w", err)
	}

	selectedMethod := mfaOption(input[InputDataFieldIDMFAMethod])

	log = log.With().Str("mfa_selected_method", string(selectedMethod)).Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("User selected MFA method")

	switch selectedMethod {
	case mfaBackup:
		input, err := d.promptUser(ctx, &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       LoginStepIDMachineMFABackup,
			Instructions: "If your authenticator app is unavailable, you can sign in with a backup code. Backup codes are meant for emergencies only.",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:        bridgev2.LoginInputFieldTypePassword,
						ID:          InputDataFieldIDMFABackupCode,
						Name:        "Backup code",
						Description: "You won’t be able to use this backup code again.",
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to prompt user for backup code: %w", err)
		}
		log.Info().Msg("Received backup code from user, proceeding")

		// Discord presents MFA backup codes to the user with dashes, but the
		// backend doesn't actually accept them. Follow in the footsteps of the
		// first party clients and remove them from the user input.
		backupCode := strings.TrimSpace(strings.ReplaceAll(
			input[InputDataFieldIDMFABackupCode],
			"-",
			"",
		))
		return &discordauth.MFAContinue{
			Type: discordauth.AuthenticatorBackup,
			MFAContinuation: discordauth.MFAContinuation{
				MFAState: challenge.MFAState,
				Code:     backupCode,
			},
		}, nil
	case mfaTotp:
		input, err := d.promptUser(ctx, &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       LoginStepIDMachineMFATOTP,
			Instructions: "Enter the code from your authenticator app.",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type: bridgev2.LoginInputFieldType2FACode,
						ID:   InputDataFieldIDMFATOTPCode,
						Name: "Authentication code",
						// TODO enforce length
						Pattern: `^(\d+)$`,
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to prompt user for TOTP code: %w", err)
		}
		log.Info().Msg("Received TOTP code from user, proceeding")

		totpCode := strings.TrimSpace(input[InputDataFieldIDMFATOTPCode])
		return &discordauth.MFAContinue{
			Type: discordauth.AuthenticatorTOTP,
			MFAContinuation: discordauth.MFAContinuation{
				MFAState: challenge.MFAState,
				Code:     totpCode,
			},
		}, nil
	case mfaSms:
		log.Info().Msg("Requesting SMS from Discord")
		_, err := challenge.RequestSMS(ctx)

		if err != nil {
			log.Err(err).Msg("Failed to request SMS from Discord")
			return nil, fmt.Errorf("failed to ask discord to send SMS: %w", err)
		}
		log.Info().Msg("Requested SMS from Discord")

		input, err := d.promptUser(ctx, &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       LoginStepIDMachineMFASMS,
			Instructions: "Enter the code Discord just texted you.",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Description: "The code might take a moment to arrive.",
						ID:          InputDataFieldIDMFASMSCode,
						Name:        "Verification code",
						// TODO enforce length
						Pattern: `^(\d+)$`,
						Type:    bridgev2.LoginInputFieldType2FACode,
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to prompt user for SMS code: %w", err)
		}
		smsCode := strings.TrimSpace(input[InputDataFieldIDMFASMSCode])
		log.Info().Msg("Received SMS code from user, proceeding")

		return &discordauth.MFAContinue{
			Type: discordauth.AuthenticatorSMS,
			MFAContinuation: discordauth.MFAContinuation{
				MFAState: challenge.MFAState,
				Code:     smsCode,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown mfa method %v", selectedMethod)
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

func (d *DiscordMachineLogin) SolveCaptcha(ctx context.Context, cap *discordauth.Captcha) (*discordauth.CaptchaSolution, error) {
	log := cap.LogContext(zerolog.Ctx(ctx).With()).Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("Encountered CAPTCHA challenge")

	if cap.Service != discordauth.CaptchaServiceHCaptcha {
		return nil, fmt.Errorf("%s captchas are currently unsupported", cap.Service)
	}

	extractJS, err := captchaExtractionJS(cap)
	if err != nil {
		return nil, fmt.Errorf("failed to compute captcha extraction JS: %w", err)
	}
	log.Debug().Str("captcha_js", extractJS).Msg("Computed CAPTCHA solution extraction JS")

	input, err := d.promptUser(ctx, &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       LoginStepIDMachineCaptcha,
		Instructions: "Discord is presenting a CAPTCHA challenge.",
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
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prompt user to solve captcha: %w", err)
	}

	solutionToken := input[CaptchaExtractionField]
	if solutionToken == "" {
		return nil, fmt.Errorf("extracted captcha solution is blank")
	}

	return &discordauth.CaptchaSolution{
		Solution: solutionToken,
	}, nil
}

func (d *DiscordMachineLogin) Cancel() {
	d.DiscordGenericLogin.Cancel()
	if d.cancelMachine != nil {
		d.cancelMachine()
	}
}

// initialCredsStep returns the very first login step needed to kick off the
// authentication flow (email or phone number and password).
func initialCredsStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDMachineInitialCreds,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   InputDataFieldIDUsernameOrPhone,
					Name: "Email or phone number",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   InputDataFieldIDPassword,
					Name: "Password",
				},
			},
		},
	}
}

func waitStep() *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepIDMachineWait,
		Instructions: "Waiting for Discord…",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeNothing,
		},
	}
}

func (d *DiscordMachineLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return initialCredsStep(""), nil
}

// initialCredsRejectionMessage reports whether err was caused by Discord
// rejecting the submitted credentials (i.e. a bad username/phone or password),
// and if so unwraps a human-readable reason suitable for re-prompting. It
// deliberately only matches errors that carry login/password field errors.
func initialCredsRejectionMessage(err error) (string, bool) {
	// TODO(skip): This needs to go. Rewrite AuthMachine to be a proper state
	// machine and obviate the connector from having to care about recognizing
	// that it should prompt the user again.

	var apiErr discordauth.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsUserInputError() {
		return "", false
	}

	// Discord returns the same "Login or password is invalid." string under
	// both the login and password keys; surface it verbatim when present.
	for _, key := range []string{"login", "password"} {
		if fieldErrs, err := apiErr.FormFieldErrors(key); err == nil && len(fieldErrs) > 0 {
			return fieldErrs[0].Message, true
		}
	}

	return "", false
}

func (d *DiscordMachineLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	return d.tryDrainingPendingPrompt(ctx, cookies), nil
}

func (d *DiscordMachineLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	log := zerolog.Ctx(ctx)

	// User input was submitted as part of a prompt that the worker signaled to
	// us.
	step := d.tryDrainingPendingPrompt(ctx, input)
	if step != nil {
		return step, nil
	}

	// Initial submission of the username/phone and password.
	username := strings.TrimSpace(input[InputDataFieldIDUsernameOrPhone])
	password := discordauth.NewSensitive(input[InputDataFieldIDPassword])
	if username == "" {
		return nil, fmt.Errorf("no username provided")
	}
	if password.IsZero() {
		return nil, fmt.Errorf("no password provided")
	}

	log.Info().Msg("Starting worker goroutine")
	err := d.startWorker(ctx, &discordauth.Creds{
		Login:    username,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start login worker: %w", err)
	}

	return waitStep(), nil
}

func (d *DiscordMachineLogin) tryDrainingPendingPrompt(ctx context.Context, input map[string]string) *bridgev2.LoginStep {
	log := zerolog.Ctx(ctx)

	d.currentlyPendingMu.Lock()
	// (Avoid holding the mutex across the channel send.)
	pending := d.currentlyPending
	d.currentlyPending = nil
	d.currentlyPendingMu.Unlock()

	if pending == nil {
		log.Debug().Msg("No pending prompt")
		return nil
	}

	log.Info().Str("pending_step_id", pending.step.StepID).
		Msg("Received user input for pending step ID, sending reply")
	pending.reply <- input

	// Go back to waiting for the worker to send a signal.
	return waitStep()
}

func (d *DiscordMachineLogin) startWorker(ctx context.Context, creds *discordauth.Creds) error {
	// Act as a sort of "mailbox"; only buffer 1 signal at a time. Not
	// unbuffered because it wouldn't be ideal to block the worker goroutine on
	// waiting for the signal to be "consumed" per se.
	d.signals = make(chan machineSignal, 1)

	// Don't want ourselves to get cancelled if the enclosing context does, but
	// we do want to preserve the data inside of the context (such as logging
	// stuff).
	//
	// Also, shadow the original context to avoid using it by accident.
	ctx, d.cancelMachine = context.WithCancel(context.WithoutCancel(ctx))
	d.machineCtx = ctx

	go func() {
		// It's important that these calls occur on a goroutine because
		// AuthMachine methods can call into our handlers (e.g. ContinueMFA),
		// which need to synchronously prompt the user, and we need both sides
		// of the reply/signal channels to work in order to avoid a deadlock.

		err := d.Machine.Prepare(ctx)
		if err != nil {
			err = fmt.Errorf("failed to prepare login: %w", err)
			_ = d.signal(d.machineCtx, machineSignal{err: err})
			return
		}

		done, err := d.Machine.Login(ctx, creds)
		log := zerolog.Ctx(ctx)
		if err == nil {
			log.Info().
				Any("required_actions", done.RequiredActions).
				Msg("Login finished")
		} else {
			// FIXME detect bad password/username and just retry the step
			// instead of failing out
			log.Err(err).Msg("Login failed")
		}

		// At the moment this can only error if we get canceled, and we don't
		// really care about that here. Just signal so we can tell bridgev2.
		_ = d.signal(d.machineCtx, machineSignal{done: done, err: err})
	}()

	return nil
}

// signal should only be called by the background goroutine, and is used to
// control the bridgev2 login process.
func (d *DiscordMachineLogin) signal(ctx context.Context, sig machineSignal) error {
	select {
	case d.signals <- sig:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// promptUser should only be called by the background goroutine, and is used to
// send a [bridgev2.LoginStep] to be presented to the user. The submitted
// inputs are collected via channel and returned.
func (d *DiscordMachineLogin) promptUser(ctx context.Context, step *bridgev2.LoginStep) (map[string]string, error) {
	reply := make(chan map[string]string, 1)
	pending := &pendingPrompt{step, reply}
	if err := d.signal(ctx, machineSignal{prompt: pending}); err != nil {
		return nil, err
	}

	select {
	case input, ok := <-pending.reply:
		if !ok {
			return nil, context.Canceled
		}
		return input, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

func (d *DiscordMachineLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	select {
	case signal := <-d.signals:
		if signal.err != nil {
			if msg, ok := initialCredsRejectionMessage(signal.err); ok {
				zerolog.Ctx(ctx).Warn().Err(signal.err).
					Msg("Discord rejected initial credentials, prompting user with initial creds step again")
				return initialCredsStep(msg), nil
			}
			return nil, signal.err
		}

		if signal.done != nil {
			return d.finalize(ctx, signal.done)
		}

		// Sanity check.
		if signal.prompt == nil {
			return nil, fmt.Errorf("unexpected empty prompt")
		}

		// Stash the prompt that we're about to show to the user so that we
		// can properly reply when mautrix calls our SubmitUserInput method.
		d.currentlyPendingMu.Lock()
		d.currentlyPending = signal.prompt
		d.currentlyPendingMu.Unlock()

		return signal.prompt.step, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
