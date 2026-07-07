package discordauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testHTTPClient func(req *http.Request) (*http.Response, error)

func (thc testHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return thc(req)
}

func newTestPersonality() *Personality {
	return &Personality{
		UserAgent:    "test-agent",
		Locale:       "en-US",
		TimeZone:     "UTC",
		DebugOptions: DefaultDebugOptions,
		SuperProperties: SuperProperties{
			OS:                "Windows",
			Browser:           "Chrome",
			BrowserUserAgent:  "test-agent",
			BrowserVersion:    "1.0.0.0",
			OSVersion:         "10",
			ReleaseChannel:    "stable",
			ClientBuildNumber: 1,
			ClientLaunchID:    "launch-id",
			ClientAppState:    "focused",
		},
	}
}

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDoAddsDebugOptionsHeader(t *testing.T) {
	var gotHeader http.Header
	client := testHTTPClient(func(req *http.Request) (*http.Response, error) {
		gotHeader = req.Header.Clone()
		return newResponse(http.StatusOK, `{"ok":true}`), nil
	})

	am := NewAuthMachine(context.Background(), client, newTestPersonality())
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/test", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := am.do(context.Background(), req)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("failed to close response body: %v", err)
	}
	if gotHeader.Get(HeaderDebugOptions) != "bugReporterEnabled" {
		t.Fatalf("expected %s header to be set, got %q", HeaderDebugOptions, gotHeader.Get(HeaderDebugOptions))
	}
}

const testCaptchaBody = `{` +
	`"captcha_key":["captcha-required"],` +
	`"captcha_service":"hcaptcha",` +
	`"captcha_sitekey":"sk",` +
	`"captcha_session_id":"sess",` +
	`"captcha_rqdata":"rqd",` +
	`"captcha_rqtoken":"rqt"` +
	`}`

const testLoginSuccessBody = `{"token":"test-token","user_id":"1234","user_settings":{"locale":"en-US"}}`

// advanceToCaptchaPrompt drives the machine from its initial state through
// credential submission, at which point the (canned) HTTP client is expected to
// answer with a CAPTCHA challenge.
func advanceToCaptchaPrompt(t *testing.T, am *AuthMachine) *Prompt {
	t.Helper()
	ctx := context.Background()

	prompt, done, err := am.Advance(ctx, nil)
	if err != nil {
		t.Fatalf("initial advance errored: %v", err)
	}
	if done != nil {
		t.Fatalf("unexpected completion on initial advance")
	}
	if prompt == nil || prompt.CredsPrompt == nil {
		t.Fatalf("expected a credentials prompt, got %+v", prompt)
	}

	prompt, done, err = am.Advance(ctx, &Answer{Creds: NewCreds("user@example.com", "hunter2")})
	if err != nil {
		t.Fatalf("advance with credentials errored: %v", err)
	}
	if done != nil {
		t.Fatalf("unexpected completion when a CAPTCHA was expected")
	}
	if prompt == nil || prompt.Captcha == nil {
		t.Fatalf("expected a CAPTCHA prompt, got %+v", prompt)
	}
	return prompt
}

// TestAdvanceCaptchaChallengeYieldsPrompt ensures that a CAPTCHA challenge
// returned mid-request surfaces as a Prompt rather than terminating the login
// with an error.
func TestAdvanceCaptchaChallengeYieldsPrompt(t *testing.T) {
	client := testHTTPClient(func(req *http.Request) (*http.Response, error) {
		return newResponse(http.StatusBadRequest, testCaptchaBody), nil
	})

	am := NewAuthMachine(context.Background(), client, newTestPersonality())
	am.Fingerprint = "test-fingerprint"

	advanceToCaptchaPrompt(t, am)
}

// TestAdvanceCaptchaSolutionRetriesWithHeader ensures that answering a CAPTCHA
// prompt retries the interrupted request with the solution and challenge state
// threaded into the request headers.
func TestAdvanceCaptchaSolutionRetriesWithHeader(t *testing.T) {
	const solution = "solved-captcha-token"

	var calls int
	var retryHeader http.Header
	client := testHTTPClient(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return newResponse(http.StatusBadRequest, testCaptchaBody), nil
		case 2:
			retryHeader = req.Header.Clone()
			return newResponse(http.StatusOK, testLoginSuccessBody), nil
		default:
			t.Fatalf("unexpected HTTP call #%d", calls)
			return nil, nil
		}
	})

	am := NewAuthMachine(context.Background(), client, newTestPersonality())
	am.Fingerprint = "test-fingerprint"

	advanceToCaptchaPrompt(t, am)

	prompt, done, err := am.Advance(context.Background(), &Answer{
		Solution: &CaptchaSolution{Solution: solution},
	})
	if err != nil {
		t.Fatalf("advance with CAPTCHA solution errored: %v", err)
	}
	if prompt != nil {
		t.Fatalf("expected login to complete, got prompt %+v", prompt)
	}
	if done == nil {
		t.Fatalf("expected a completed login")
	}
	if got := done.Token.UnwrapSensitive(); got != "test-token" {
		t.Fatalf("unexpected token %q", got)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 HTTP calls, got %d", calls)
	}
	if got := retryHeader.Get(HeaderCaptchaKey); got != solution {
		t.Fatalf("expected %s header %q on retry, got %q", HeaderCaptchaKey, solution, got)
	}
	if got := retryHeader.Get(HeaderCaptchaSessionID); got != "sess" {
		t.Fatalf("expected %s header %q on retry, got %q", HeaderCaptchaSessionID, "sess", got)
	}
}
