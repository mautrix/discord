package discordauth

import (
	"context"
	"encoding/json"
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

const (
	testLoginSuccessBody            = `{"token":"test-token","user_id":"1234","user_settings":{"locale":"en-US"}}`
	testPhoneVerificationNeededBody = `{"code":70007,"message":""}`
	testInvalidFormBody             = `{"code":50035,"message":""}`

	testIPVerificationToken = "foobar123"
	testVerifiedPhone       = `{"token":"` + testIPVerificationToken + `"}`
)

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

func TestIPVerificationViaSMS(t *testing.T) {
	const (
		loginPhone = "+15555550123"
		password   = "hunter2"
		wrongCode  = "111111"
		rightCode  = "222222"
	)

	var calls int
	client := testHTTPClient(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			expectPostRequest(t, req, "/api/v9/auth/login")
			expectLoginRequestBody(t, req, loginPhone, password)
			return newResponse(http.StatusBadRequest, testPhoneVerificationNeededBody), nil
		case 2:
			expectPostRequest(t, req, "/api/v9/phone-verifications/verify")
			expectPhoneVerificationRequestBody(t, req, loginPhone, wrongCode)
			return newResponse(http.StatusBadRequest, testInvalidFormBody), nil
		case 3:
			expectPostRequest(t, req, "/api/v9/phone-verifications/verify")
			expectPhoneVerificationRequestBody(t, req, loginPhone, rightCode)
			return newResponse(http.StatusOK, testVerifiedPhone), nil
		case 4:
			expectPostRequest(t, req, "/api/v9/auth/authorize-ip")
			expectAuthorizeIPRequestBody(t, req, testIPVerificationToken)
			return newResponse(http.StatusNoContent, ""), nil
		case 5:
			expectPostRequest(t, req, "/api/v9/auth/login")
			expectLoginRequestBody(t, req, loginPhone, password)
			return newResponse(http.StatusOK, testLoginSuccessBody), nil
		default:
			t.Fatalf("unexpected HTTP call #%d", calls)
			return nil, nil
		}
	})

	ctx := context.Background()

	am := NewAuthMachine(ctx, client, newTestPersonality())
	am.Fingerprint = "test-fingerprint"

	prompt, done := mustAdvance(t, ctx, am, nil)
	if done != nil || prompt == nil || prompt.CredsPrompt == nil {
		t.Fatalf("expected credentials prompt, got prompt=%+v done=%+v", prompt, done)
	}

	prompt, done = mustAdvance(t, ctx, am, &Answer{
		Creds: NewCreds(loginPhone, password),
	})
	phonePrompt := expectPhoneVerifyPrompt(t, prompt, done)
	if phonePrompt.Phone != loginPhone {
		t.Fatalf("expected phone verify prompt for %q, got %q", loginPhone, phonePrompt.Phone)
	}
	if phonePrompt.Retrying {
		t.Fatal("initial phone verify prompt should not be retrying")
	}

	prompt, done = mustAdvance(t, ctx, am, &Answer{
		SMSCode: wrongCode,
	})
	phonePrompt = expectPhoneVerifyPrompt(t, prompt, done)
	if !phonePrompt.Retrying {
		t.Fatal("phone verify prompt should be retrying")
	}

	prompt, completed := mustAdvance(t, ctx, am, &Answer{
		SMSCode: rightCode,
	})
	if prompt != nil {
		t.Fatalf("unexpected prompt: %+v", prompt)
	}
	if completed == nil {
		t.Fatal("expected login to complete")
	}
	if got := completed.Token.UnwrapSensitive(); got != "test-token" {
		t.Fatalf("unexpected token %q", got)
	}
	if calls != 5 {
		t.Fatalf("expected exactly 5 HTTP calls, got %d", calls)
	}
}

func mustAdvance(
	t *testing.T,
	ctx context.Context,
	am *AuthMachine,
	answer *Answer,
) (*Prompt, *LoginCompleted) {
	t.Helper()

	prompt, done, err := am.Advance(ctx, answer)
	if err != nil {
		t.Fatalf("advance returned error: %v", err)
	}
	return prompt, done
}

func expectPhoneVerifyPrompt(t *testing.T, prompt *Prompt, done *LoginCompleted) *PhoneVerifyPrompt {
	t.Helper()

	if done != nil || prompt == nil || prompt.PhoneVerifyPrompt == nil {
		t.Fatalf("expected phone verify prompt, got prompt=%+v done=%+v", prompt, done)
	}
	return prompt.PhoneVerifyPrompt
}

func expectPostRequest(t *testing.T, req *http.Request, path string) {
	t.Helper()

	if req.Method != http.MethodPost {
		t.Fatalf("expected POST request, got %s", req.Method)
	}
	if req.URL.Path != path {
		t.Fatalf("expected request path %q, got %q", path, req.URL.Path)
	}
}

func expectLoginRequestBody(t *testing.T, req *http.Request, login string, password string) {
	t.Helper()

	var got struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	mustDecodeRequestJSON(t, req, &got)

	if got.Login != login {
		t.Fatalf("expected login %q, got %q", login, got.Login)
	}
	if got.Password != password {
		t.Fatalf("expected password %q, got %q", password, got.Password)
	}
}

func expectPhoneVerificationRequestBody(t *testing.T, req *http.Request, phone string, code string) {
	t.Helper()

	var got VerifyPhoneNumberRequest
	mustDecodeRequestJSON(t, req, &got)

	if got.Phone != phone {
		t.Fatalf("expected phone %q, got %q", phone, got.Phone)
	}
	if got.Code != code {
		t.Fatalf("expected code %q, got %q", code, got.Code)
	}
}

func expectAuthorizeIPRequestBody(t *testing.T, req *http.Request, token string) {
	t.Helper()

	var got struct {
		Token string `json:"token"`
	}
	mustDecodeRequestJSON(t, req, &got)

	if got.Token != token {
		t.Fatalf("expected IP verification token %q, got %q", token, got.Token)
	}
}

func mustDecodeRequestJSON(t *testing.T, req *http.Request, v any) {
	t.Helper()

	defer req.Body.Close()
	if err := json.NewDecoder(req.Body).Decode(v); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
}
