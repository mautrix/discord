package discordauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testHTTPClient func(req *http.Request) (*http.Response, error)

func (thc testHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return thc(req)
}

type testChallengeHandler struct{}

func (testChallengeHandler) SolveCaptcha(context.Context, *Captcha) (*CaptchaSolution, error) {
	return &CaptchaSolution{Solution: "test-captcha-solution"}, nil
}

func (testChallengeHandler) ContinueMFA(context.Context, *MFAChallenge) (*MFAContinue, error) {
	return nil, errors.New("unexpected MFA continuation in test")
}

func (testChallengeHandler) WaitForEmailVerification(context.Context) error {
	return errors.New("unexpected email verification in test")
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

func TestDoHandlingCaptchaAddsDebugOptionsHeader(t *testing.T) {
	var gotHeader http.Header
	client := testHTTPClient(func(req *http.Request) (*http.Response, error) {
		gotHeader = req.Header.Clone()
		return newResponse(http.StatusOK, `{"ok":true}`), nil
	})

	am := NewAuthMachine(context.Background(), client, newTestPersonality(), testChallengeHandler{})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/test", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	_, _, err = am.doHandlingCaptcha(context.Background(), req)
	if err != nil {
		t.Fatalf("doHandlingCaptcha returned error: %v", err)
	}
	if gotHeader.Get(HeaderDebugOptions) != "bugReporterEnabled" {
		t.Fatalf("expected %s header to be set, got %q", HeaderDebugOptions, gotHeader.Get(HeaderDebugOptions))
	}
}
