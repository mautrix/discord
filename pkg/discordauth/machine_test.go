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
