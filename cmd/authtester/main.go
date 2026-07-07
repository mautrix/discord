package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/term"

	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-discord/pkg/discordauth"
	"go.mau.fi/mautrix-discord/pkg/discordtransport"
)

const fallbackClientBuildNumber = 497254

var mainJSRegex = regexp.MustCompile(`src="(/assets/web\.[a-f0-9]{12,32}\.js)"`)
var buildNumberRegex = regexp.MustCompile(`(?:buildNumber|build_number):\s?['"]?(\d{6,})['"]?`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var buildNumberFlag int
	var apiBase string
	var verbose bool

	flag.IntVar(&buildNumberFlag, "build-number", 0, "Discord client build number (default: auto-detect from discord.com)")
	flag.StringVar(&apiBase, "api-base", "https://discord.com/api/v9", "Discord API base URL")
	flag.BoolVar(&verbose, "verbose", false, "Lower the log level to debug")
	flag.Parse()

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		Level(zerolog.InfoLevel).
		With().
		Timestamp().
		Logger()
	if verbose {
		log = log.Level(zerolog.DebugLevel)
	}

	ctx, stop := signal.NotifyContext(log.WithContext(context.Background()), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := discordtransport.CompileTransport(exhttp.SensibleClientSettings, discordtransport.TransportOptions{})
	if err != nil {
		return fmt.Errorf("failed to create auth http client: %w", err)
	}

	captchaServer := newCaptchaServer(log.With().Str("component", "authtester captcha").Logger())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := captchaServer.Close(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to gracefully terminate CAPTCHA server: %v\n", err)
		}
	}()
	prompter := newPrompter(os.Stdin, os.Stdout, captchaServer)

	buildNumber := buildNumberFlag
	if buildNumber == 0 {
		fmt.Fprintln(os.Stdout, "Detecting an appropriate Discord client build number...")
		buildNumber, err = fetchClientBuildNumber(ctx, client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to detect build number automatically: %v\n", err)
			fmt.Fprintf(os.Stderr, "Falling back to build number %d\n", fallbackClientBuildNumber)
			buildNumber = fallbackClientBuildNumber
		}
	}
	fmt.Fprintf(os.Stdout, "Using client build number %d\n", buildNumber)

	personality, err := newDefaultPersonality(buildNumber)
	if err != nil {
		return fmt.Errorf("failed to create auth personality: %w", err)
	}

	machine := discordauth.NewAuthMachine(ctx, client, personality)
	machine.APIBase = apiBase

	fmt.Fprintln(os.Stdout, "Preparing Discord auth...")
	if err = machine.Prepare(ctx); err != nil {
		return fmt.Errorf("failed to prepare auth machine: %w", err)
	}

	fmt.Fprintln(os.Stdout, "Logging in...")
	resp, err := prompter.driveAuthMachine(ctx, machine)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	fmt.Fprintln(os.Stdout, "Login succeeded.")
	fmt.Fprintf(os.Stdout, "User ID: %s\n", resp.UserID)
	fmt.Fprintf(os.Stdout, "Token length: %d\n", len(resp.Token.UnwrapSensitive()))
	if resp.UserSettings.Locale != "" {
		fmt.Fprintf(os.Stdout, "User locale: %s\n", resp.UserSettings.Locale)
	}
	if resp.UserSettings.Theme != "" {
		fmt.Fprintf(os.Stdout, "User theme: %s\n", resp.UserSettings.Theme)
	}

	return nil
}

func newDefaultPersonality(buildNumber int) (*discordauth.Personality, error) {
	launchSignature, err := discordgo.NewVanillaSignature()
	if err != nil {
		return nil, fmt.Errorf("failed to generate launch signature: %w", err)
	}

	extraHeaders := maps.Clone(discordgo.DroidFetchHeaders)
	delete(extraHeaders, "User-Agent")

	return &discordauth.Personality{
		UserAgent:    discordgo.DroidBrowserUserAgent,
		Locale:       "en-US",
		TimeZone:     defaultTimeZone(),
		DebugOptions: discordauth.DefaultDebugOptions,
		SuperProperties: discordauth.SuperProperties{
			OS:                "Windows",
			Browser:           "Chrome",
			SystemLocale:      "en-US",
			HasClientMods:     false,
			BrowserUserAgent:  discordgo.DroidBrowserUserAgent,
			BrowserVersion:    discordgo.DroidBrowserVersion,
			OSVersion:         "10",
			ReleaseChannel:    "stable",
			ClientBuildNumber: buildNumber,
			ClientLaunchID:    uuid.NewString(),
			LaunchSignature:   launchSignature,
			ClientAppState:    "focused",
		},
		ExtraHeaders: extraHeaders,
	}, nil
}

func defaultTimeZone() string {
	timeZone := time.Now().Location().String()
	if timeZone == "" || timeZone == "Local" {
		return "UTC"
	}

	return timeZone
}

func fetchClientBuildNumber(ctx context.Context, client *http.Client) (int, error) {
	mainPageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/channels/@me", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create main page request: %w", err)
	}
	addHeaders(mainPageReq.Header, discordgo.DroidBaseHeaders)
	mainPageReq.Header.Set("Sec-Fetch-Dest", "document")
	mainPageReq.Header.Set("Sec-Fetch-Mode", "navigate")
	mainPageReq.Header.Set("Sec-Fetch-Site", "none")
	mainPageReq.Header.Set("Sec-Fetch-User", "?1")
	mainPageReq.Header.Set("Upgrade-Insecure-Requests", "1")
	mainPageReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")

	mainPageData, err := doRequest(ctx, client, mainPageReq)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch main page: %w", err)
	}

	mainJSMatch := mainJSRegex.FindSubmatch(mainPageData)
	if mainJSMatch == nil {
		return 0, fmt.Errorf("failed to find main JS URL in Discord main page")
	}

	jsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com"+string(mainJSMatch[1]), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create JS request: %w", err)
	}
	addHeaders(jsReq.Header, discordgo.DroidBaseHeaders)
	jsReq.Header.Set("Sec-Fetch-Dest", "script")
	jsReq.Header.Set("Sec-Fetch-Mode", "no-cors")
	jsReq.Header.Set("Sec-Fetch-Site", "same-origin")
	jsReq.Header.Set("Accept", "*/*")

	jsData, err := doRequest(ctx, client, jsReq)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch main JS: %w", err)
	}

	buildNumberMatch := buildNumberRegex.FindSubmatch(jsData)
	if buildNumberMatch == nil {
		return 0, fmt.Errorf("failed to find build number in Discord JS bundle")
	}

	buildNumber, err := strconv.Atoi(string(buildNumberMatch[1]))
	if err != nil {
		return 0, fmt.Errorf("failed to parse build number %q: %w", buildNumberMatch[1], err)
	}

	return buildNumber, nil
}

func doRequest(ctx context.Context, client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s %s response body: %w", req.Method, req.URL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %s for %s %s", resp.Status, req.Method, req.URL)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return body, nil
}

func addHeaders(header http.Header, values map[string]string) {
	for key, value := range values {
		header.Set(key, value)
	}
}

type prompter struct {
	in            *bufio.Reader
	inFile        *os.File
	out           io.Writer
	captchaServer *captchaServer

	mfaChallenge *discordauth.LoginMFARequired
}

type mfaMethodOption struct {
	Type       discordauth.AuthenticatorType
	Label      string
	CodePrompt string
}

func newPrompter(in io.Reader, out io.Writer, captchaServer *captchaServer) *prompter {
	file, _ := in.(*os.File)

	return &prompter{
		in:            bufio.NewReader(in),
		inFile:        file,
		out:           out,
		captchaServer: captchaServer,
	}
}

func (p *prompter) promptRequired(label string) (string, error) {
	value, err := p.prompt(label)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(label))
	}

	return value, nil
}

func (p *prompter) prompt(label string) (string, error) {
	fmt.Fprintf(p.out, "%s: ", label)
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return "", io.EOF
	}

	return strings.TrimRight(line, "\r\n"), nil
}

func (p *prompter) promptSecretRequired(label string) (string, error) {
	value, err := p.promptSecret(label)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(label))
	}

	return value, nil
}

func (p *prompter) promptSecret(label string) (string, error) {
	if p.inFile == nil || !term.IsTerminal(int(p.inFile.Fd())) {
		return p.prompt(label)
	}

	fmt.Fprintf(p.out, "%s: ", label)
	line, err := term.ReadPassword(int(p.inFile.Fd()))
	fmt.Fprintln(p.out)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(string(line), "\r\n"), nil
}

func (p *prompter) promptMFAChoice(options []mfaMethodOption) (mfaMethodOption, error) {
	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, "Available MFA methods:")
	for i, option := range options {
		fmt.Fprintf(p.out, "  %d. %s\n", i+1, option.Label)
	}

	for {
		choice, err := p.promptRequired("Choose MFA method")
		if err != nil {
			return mfaMethodOption{}, err
		}

		index, err := strconv.Atoi(choice)
		if err == nil && index >= 1 && index <= len(options) {
			return options[index-1], nil
		}

		fmt.Fprintf(p.out, "Invalid choice %q. Enter a number from 1 to %d.\n", choice, len(options))
	}
}

func (p *prompter) driveAuthMachine(ctx context.Context, machine *discordauth.AuthMachine) (*discordauth.LoginCompleted, error) {
	var answer *discordauth.Answer
	for {
		prompt, done, err := machine.Advance(ctx, answer)
		if err != nil {
			return nil, err
		}
		if done != nil {
			return done, nil
		}
		if prompt == nil {
			return nil, fmt.Errorf("auth machine did not advance")
		}

		answer, err = p.answerPrompt(ctx, prompt)
		if err != nil {
			return nil, err
		}
	}
}

func (p *prompter) answerPrompt(ctx context.Context, prompt *discordauth.Prompt) (*discordauth.Answer, error) {
	switch {
	case prompt.CredsPrompt != nil:
		return p.answerCreds(prompt.CredsPrompt)
	case prompt.Captcha != nil:
		solution, err := p.SolveCaptcha(ctx, prompt.Captcha)
		if err != nil {
			return nil, err
		}
		return &discordauth.Answer{Solution: solution}, nil
	case prompt.EmailVerify:
		if err := p.WaitForEmailVerification(ctx); err != nil {
			return nil, err
		}
		return &discordauth.Answer{}, nil
	case prompt.MFAChallengePrompt != nil:
		return p.answerMFAMethod(prompt.MFAChallengePrompt)
	case prompt.MFACodePrompt != nil:
		return p.answerMFACode(prompt.MFACodePrompt)
	default:
		return nil, fmt.Errorf("auth machine returned an empty prompt")
	}
}

func (p *prompter) answerCreds(prompt *discordauth.CredsPrompt) (*discordauth.Answer, error) {
	if prompt.Reason != "" {
		fmt.Fprintln(p.out)
		fmt.Fprintf(p.out, "Discord rejected those credentials: %s\n", prompt.Reason)
	}

	login, err := p.promptRequired("Email or phone")
	if err != nil {
		return nil, fmt.Errorf("failed to read login: %w", err)
	}
	password, err := p.promptSecretRequired("Password")
	if err != nil {
		return nil, fmt.Errorf("failed to read password: %w", err)
	}

	return &discordauth.Answer{
		Creds: discordauth.NewCreds(login, password),
	}, nil
}

func supportedMFAMethods(challenge *discordauth.LoginMFARequired) []mfaMethodOption {
	options := make([]mfaMethodOption, 0, 3)
	if challenge.TOTPEnabled {
		options = append(options, mfaMethodOption{
			Type:       discordauth.AuthenticatorTOTP,
			Label:      "TOTP authenticator",
			CodePrompt: "TOTP code",
		})
	}
	if challenge.SMSEnabled {
		options = append(options, mfaMethodOption{
			Type:       discordauth.AuthenticatorSMS,
			Label:      "SMS code",
			CodePrompt: "SMS code",
		})
	}
	if challenge.BackupCodesAccepted {
		options = append(options, mfaMethodOption{
			Type:       discordauth.AuthenticatorBackup,
			Label:      "Backup code",
			CodePrompt: "Backup code",
		})
	}

	return options
}

func newMFAContinue(challenge *discordauth.LoginMFARequired, authType discordauth.AuthenticatorType, code string) *discordauth.MFAContinue {
	return &discordauth.MFAContinue{
		Type: authType,
		MFAContinuation: discordauth.MFAContinuation{
			MFAState: challenge.MFAState,
			Code:     code,
		},
	}
}

func (p *prompter) answerMFAMethod(prompt *discordauth.MFAChallengePrompt) (*discordauth.Answer, error) {
	challenge := prompt.LoginMFARequired
	if challenge == nil {
		return nil, fmt.Errorf("auth machine returned an MFA prompt without a challenge")
	}

	if prompt.Reason != "" {
		fmt.Fprintln(p.out)
		fmt.Fprintf(p.out, "Discord rejected that MFA response: %s\n", prompt.Reason)
	}

	options := supportedMFAMethods(challenge)
	if len(options) == 0 {
		if challenge.WebAuthnCredential != nil {
			panic("authtester does not support WebAuthn MFA")
		}
		return nil, fmt.Errorf("discord did not offer a supported MFA method")
	}

	selected := options[0]
	if len(options) == 1 {
		fmt.Fprintln(p.out)
		fmt.Fprintf(p.out, "Using MFA method: %s\n", selected.Label)
	} else {
		var err error
		selected, err = p.promptMFAChoice(options)
		if err != nil {
			return nil, err
		}
	}

	if selected.Type == discordauth.AuthenticatorSMS {
		fmt.Fprintln(p.out)
		fmt.Fprintln(p.out, "Requesting an MFA SMS code...")
	}

	p.mfaChallenge = challenge
	return &discordauth.Answer{
		PickedMFAType: &selected.Type,
	}, nil
}

func (p *prompter) answerMFACode(prompt *discordauth.MFACodePrompt) (*discordauth.Answer, error) {
	if p.mfaChallenge == nil {
		return nil, fmt.Errorf("auth machine requested an MFA code before an MFA challenge")
	}

	selected := mfaMethodOption{
		Type: prompt.Type,
	}
	switch prompt.Type {
	case discordauth.AuthenticatorTOTP:
		selected.CodePrompt = "TOTP code"
	case discordauth.AuthenticatorSMS:
		fmt.Fprintln(p.out, "Discord sent an MFA SMS code.")
		selected.CodePrompt = "SMS code"
	case discordauth.AuthenticatorBackup:
		selected.CodePrompt = "Backup code"
	default:
		return nil, fmt.Errorf("unsupported MFA code prompt type %q", prompt.Type)
	}

	code, err := p.promptSecretRequired(selected.CodePrompt)
	if err != nil {
		return nil, err
	}
	if selected.Type == discordauth.AuthenticatorBackup {
		code = strings.ReplaceAll(code, "-", "")
	}

	return &discordauth.Answer{
		MFAContinue: newMFAContinue(p.mfaChallenge, selected.Type, code),
	}, nil
}

func (p *prompter) SolveCaptcha(ctx context.Context, captcha *discordauth.Captcha) (*discordauth.CaptchaSolution, error) {
	captchaData, err := json.MarshalIndent(captcha, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode captcha challenge: %w", err)
	}

	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, "Received CAPTCHA challenge:")
	fmt.Fprintln(p.out, string(captchaData))

	if p.captchaServer != nil && supportsBrowserCaptcha(captcha) {
		pageURL, waitForSolution, err := p.captchaServer.startChallenge(captcha)
		if err != nil {
			fmt.Fprintf(p.out, "Failed to start local CAPTCHA page: %v\n", err)
			fmt.Fprintln(p.out, "Falling back to manual token entry.")
		} else {
			fmt.Fprintln(p.out)
			fmt.Fprintln(p.out, "Open this page in your browser and solve the CAPTCHA:")
			fmt.Fprintf(p.out, "  %s\n", pageURL)
			fmt.Fprintln(p.out, "If the page reports an error or you cancel it, authtester will fall back to manual token entry.")

			solution, err := waitForSolution(ctx)
			switch {
			case err == nil:
				return &discordauth.CaptchaSolution{Solution: solution}, nil
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return nil, err
			case errors.Is(err, errCaptchaBrowserCanceled):
				fmt.Fprintln(p.out, "Local CAPTCHA page was canceled.")
				fmt.Fprintln(p.out, "Falling back to manual token entry.")
			default:
				fmt.Fprintf(p.out, "Local CAPTCHA page failed: %v\n", err)
				fmt.Fprintln(p.out, "Falling back to manual token entry.")
			}
		}
	} else {
		fmt.Fprintln(p.out, "Local browser flow only supports hCaptcha challenges with a sitekey.")
	}

	solution, err := p.promptRequired("CAPTCHA solution")
	if err != nil {
		return nil, err
	}

	return &discordauth.CaptchaSolution{Solution: solution}, nil
}

func (p *prompter) WaitForEmailVerification(ctx context.Context) error {
	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, "Discord detected a login from a new location and emailed a")
	fmt.Fprintln(p.out, "verification link. Open the email, click the link to authorize")
	fmt.Fprintln(p.out, "this login, then continue.")

	// The return value is irrelevant; we just block until the user confirms.
	_, err := p.prompt("Press enter once you've verified")
	return err
}
