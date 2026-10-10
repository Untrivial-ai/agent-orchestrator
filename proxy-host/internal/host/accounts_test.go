package host

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// accountFixture holds one account of the provider and scripts what the
// provider answers to calls made as it.
func accountFixture(t *testing.T, provider *fakeProvider, auth *coreauth.Auth) *fixture {
	t.Helper()
	f, _ := bareFixture(t, provider)
	auth.ID, auth.Provider = "account", provider.name
	f.account(auth)
	return f
}
func statuses(codes ...int) func(*http.Request, string) (int, string) {
	call := 0
	return func(*http.Request, string) (int, string) {
		code := codes[min(call, len(codes)-1)]
		call++
		return code, `{"plan_type":"pro"}`
	}
}
func providerCallBody(provider, method, url, body string) string {
	if body != "" {
		body = `,"body":` + body
	}
	return `{"auth_id":"account","provider":"` + provider + `","method":"` + method + `","url":"` + url + `"` + body + `}`
}

func TestProviderCallRenewsARefusedSignInOnceAndNeverAnAPIKey(t *testing.T) {
	usage := providerCallBody("codex", "GET", "https://chatgpt.com/backend-api/wham/usage", "")
	for name, tc := range map[string]struct {
		provider       *fakeProvider
		apiKey         bool
		code           int
		body           string
		calls, renewal int
		healthy        bool
	}{
		"a healthy account is not renewed":           {&fakeProvider{answer: statuses(200)}, false, 200, `{"body":{"plan_type":"pro"},"status":200}`, 1, 0, true},
		"a refused sign-in is renewed and retried":   {&fakeProvider{answer: statuses(401, 200)}, false, 200, `{"body":{"plan_type":"pro"},"status":200}`, 2, 1, true},
		"a second refusal is passed on":              {&fakeProvider{answer: statuses(401)}, false, 200, `{"body":{"plan_type":"pro"},"status":401}`, 2, 1, true},
		"a renewal the provider refuses is recorded": {&fakeProvider{answer: statuses(401), refuse: &coreauth.Error{Code: "unauthorized", Message: "unauthorized", HTTPStatus: 401}}, false, 502, ``, 1, 1, false},
		"an API key is never renewed":                {&fakeProvider{answer: statuses(401)}, true, 200, `{"body":{"plan_type":"pro"},"status":401}`, 1, 0, true},
		"a provider out of reach":                    {&fakeProvider{}, false, 502, ``, 1, 0, true},
		"another status is passed on, not retried":   {&fakeProvider{answer: statuses(429)}, false, 200, `{"body":{"plan_type":"pro"},"status":429}`, 1, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			tc.provider.name = "codex"
			auth := &coreauth.Auth{}
			if tc.apiKey {
				auth.Attributes = map[string]string{"api_key": "sk-test"}
			}
			f := accountFixture(t, tc.provider, auth)
			response := f.send(http.MethodPost, "/ao/provider-call", control, usage)
			if response.Code != tc.code || strings.TrimSpace(response.Body.String()) != tc.body {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(tc.provider.requests) != tc.calls || tc.provider.refreshes != tc.renewal {
				t.Fatalf("calls=%d renewals=%d", len(tc.provider.requests), tc.provider.refreshes)
			}
			// AO reads a dead sign-in from the account's own state, which the SDK's renewal sets.
			if got, _ := f.manager.GetByID("account"); (got.Status == coreauth.StatusActive) != tc.healthy {
				t.Fatalf("account status is %q", got.Status)
			}
		})
	}
}

func TestProviderCallSendsTheRequestAsGivenWithTheProvidersHeaders(t *testing.T) {
	var got struct {
		method, url, body string
		header            http.Header
	}
	answer := `{"result":"reset"}`
	provider := &fakeProvider{name: "claude", answer: func(r *http.Request, body string) (int, string) {
		got.method, got.url, got.body, got.header = r.Method, r.URL.String(), body, r.Header
		return http.StatusAccepted, answer
	}}
	f := accountFixture(t, provider, &coreauth.Auth{})
	claim := "https://api.anthropic.com/api/organizations/0b9d2c5e/reset_rate_limits?x=1"
	response := f.send(http.MethodPost, "/ao/provider-call", control, providerCallBody("claude", "POST", claim, `{"grant_id":"g2","n":1240000000000}`))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"body":{"result":"reset"},"status":202}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got.method != "POST" || got.url != claim || got.body != `{"grant_id":"g2","n":1240000000000}` {
		t.Fatalf("the provider received %s %s %s", got.method, got.url, got.body)
	}
	if got.header.Get("Accept") != "application/json" || got.header.Get("anthropic-beta") != "oauth-2025-04-20" || got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers=%v", got.header)
	}
	// A read has no body and no content type; an answer that is not JSON comes back as text.
	answer = "busy, try later"
	for _, body := range []string{"", "null"} {
		response = f.send(http.MethodPost, "/ao/provider-call", control, providerCallBody("claude", "GET", "https://api.anthropic.com/api/oauth/profile", body))
		if strings.TrimSpace(response.Body.String()) != `{"body":"busy, try later","status":202}` || got.body != "" || got.header.Get("Content-Type") != "" {
			t.Fatalf("body=%s sent %q with %v", response.Body.String(), got.body, got.header)
		}
	}
	codex := &fakeProvider{name: "codex", answer: func(r *http.Request, _ string) (int, string) { got.header = r.Header; return 200, "" }}
	response = accountFixture(t, codex, &coreauth.Auth{}).send(http.MethodPost, "/ao/provider-call", control, providerCallBody("codex", "GET", "https://chatgpt.com/backend-api/wham/usage", ""))
	if strings.TrimSpace(response.Body.String()) != `{"body":"","status":200}` || got.header.Get("anthropic-beta") != "" {
		t.Fatalf("body=%s headers=%v", response.Body.String(), got.header)
	}
}

func TestProviderCallGoesOnlyToTheAccountsOwnProvider(t *testing.T) {
	provider := &fakeProvider{name: "codex", answer: statuses(200)}
	f := accountFixture(t, provider, &coreauth.Auth{})
	for _, url := range []string{"https://api.anthropic.com/api/oauth/usage", "http://chatgpt.com/backend-api/wham/usage", "https://chatgpt.com.evil.test/backend-api/wham/usage",
		"https://chatgpt.com@evil.test/", "https://evil.test/https://chatgpt.com/", "https://chatgpt.com", "//chatgpt.com/x", " https://chatgpt.com/x", ""} {
		if response := f.send(http.MethodPost, "/ao/provider-call", control, providerCallBody("codex", "GET", url, "")); response.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", url, response.Code)
		}
	}
	for body, status := range map[string]int{
		providerCallBody("claude", "GET", "https://api.anthropic.com/api/oauth/usage", ""):                          http.StatusNotFound,
		strings.Replace(providerCallBody("codex", "GET", "https://chatgpt.com/x", ""), `"account"`, `"missing"`, 1): http.StatusNotFound,
		`not json`: http.StatusBadRequest,
	} {
		if response := f.send(http.MethodPost, "/ao/provider-call", control, body); response.Code != status {
			t.Fatalf("%s: %d, want %d", body, response.Code, status)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatalf("a refused call reached the provider: %d", len(provider.requests))
	} // Every call the daemon makes today is on its provider's origin.
	for name, urls := range map[string][]string{
		"codex": {"https://chatgpt.com/backend-api/wham/usage", "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", "https://chatgpt.com/backend-api/wham/profiles/me",
			"https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"},
		"claude": {"https://api.anthropic.com/api/oauth/usage", "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1", "https://api.anthropic.com/api/oauth/profile",
			"https://api.anthropic.com/api/organizations/0b9d2c5e-3f4a-4b6c-8d7e-9f0a1b2c3d4e/reset_rate_limits"},
	} {
		allowed := accountFixture(t, &fakeProvider{name: name, answer: statuses(200)}, &coreauth.Auth{})
		for _, url := range urls {
			if response := allowed.send(http.MethodPost, "/ao/provider-call", control, providerCallBody(name, "POST", url, `{}`)); response.Code != http.StatusOK {
				t.Fatalf("%s: %d", url, response.Code)
			}
		}
	}
}

func idToken(claims string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
}

func TestAccountStateReportsLocalFactsUnderTheUsageTypesNames(t *testing.T) {
	now := time.Now()
	paused := now.Add(time.Hour).UTC().Truncate(time.Second)
	f := accountFixture(t, &fakeProvider{name: "codex"}, &coreauth.Auth{
		CreatedAt: time.Date(2029, 9, 3, 8, 0, 0, 0, time.UTC),
		Metadata: map[string]any{"last_refresh": "2030-01-01T11:48:00+05:30",
			"id_token": idToken(`{"https://api.openai.com/auth":{"chatgpt_subscription_active_until":"2030-02-01T00:00:00+00:00"}}`)},
		Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: paused},
	})
	response := f.send(http.MethodPost, "/ao/account-state", control, `{"auth_id":"account","provider":"codex"}`)
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	requests, _ := state["requests"].([]any)
	if len(requests) != 20 || !reflect.DeepEqual(requests[0], map[string]any{"succeeded": float64(0), "failed": float64(0)}) {
		t.Fatalf("requests=%v", state["requests"])
	}
	delete(state, "requests")
	want := map[string]any{"addedAt": "2029-09-03T08:00:00Z", "refreshedAt": "2030-01-01T06:18:00Z", "pausedUntil": paused.Format(time.RFC3339), "pausedReason": "credential_quota", "renewsAt": "2030-02-01T00:00:00Z"}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state=%v\nwant %v", state, want)
	}
	if response := f.send(http.MethodPost, "/ao/account-state", control, `{"auth_id":"missing","provider":"codex"}`); response.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", response.Code)
	}
	// Resuming lifts the pause.
	if response := f.send(http.MethodPost, "/ao/account-resume", control, `{"auth_id":"account","provider":"codex"}`); response.Code != http.StatusNoContent {
		t.Fatalf("resume: %d %s", response.Code, response.Body.String())
	}
	if got, _ := f.manager.GetByID("account"); accountState(got, now)["pausedUntil"] != nil {
		t.Fatal("the account is still paused after a resume")
	}
}

func TestAccountStateOmitsWhatDoesNotApply(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for name, auth := range map[string]*coreauth.Auth{
		"nothing known": {ID: "a", Provider: "claude"},
		"an API key": {ID: "a", Provider: "codex", Attributes: map[string]string{"api_key": "k"}, Runtime: renewsBefore(4 * time.Hour),
			Metadata: map[string]any{"expired": now.Add(time.Hour).Format(time.RFC3339)}},
		"a sign-in failure is not a pause": {ID: "a", Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Hour),
			LastError: &coreauth.Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized}},
		"a pause that has run out": {ID: "a", Provider: "codex", Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(-time.Minute)}},
		"a token without a plan":   {ID: "a", Provider: "codex", Metadata: map[string]any{"id_token": idToken(`{"email":"a@example.test"}`), "last_refresh": "never"}},
		"a malformed token":        {ID: "a", Provider: "codex", Metadata: map[string]any{"id_token": "one.!!!.three"}},
	} {
		state := accountState(auth, now)
		if len(state) != 1 || state["requests"] == nil {
			t.Fatalf("%s: state=%v", name, state)
		}
	}
	// A plan end saved as Unix seconds reads the same as one saved as text.
	numeric := accountState(&coreauth.Auth{ID: "a", Provider: "codex", Metadata: map[string]any{"id_token": idToken(`{"https://api.openai.com/auth":{"chatgpt_subscription_active_until":1896134400}}`)}}, now)
	if numeric["renewsAt"] != "2030-02-01T00:00:00Z" {
		t.Fatalf("renewsAt=%v", numeric["renewsAt"])
	}
}

// renewsBefore stands in for a provider's rule of renewing a sign-in this long
// before its token runs out.
type renewsBefore time.Duration

func (r renewsBefore) RefreshLead() *time.Duration { lead := time.Duration(r); return &lead }

func TestSignInEndingIsReportedOnceRenewalIsWellOverdue(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	expiresIn := func(left time.Duration) map[string]any {
		return map[string]any{"expired": now.Add(left).Format(time.RFC3339)}
	}
	renewed := func(ago time.Duration) map[string]any {
		return map[string]any{"last_refresh": now.Add(-ago).Format(time.RFC3339)}
	}
	for name, tc := range map[string]struct {
		auth   coreauth.Auth
		ending bool
		until  string
	}{
		"not yet due for renewal":           {coreauth.Auth{Metadata: expiresIn(6 * time.Hour)}, false, ""},
		"due, and the SDK is on it":         {coreauth.Auth{Metadata: expiresIn(4*time.Hour - 5*time.Minute)}, false, ""},
		"due half an hour ago, unrenewed":   {coreauth.Auth{Metadata: expiresIn(3 * time.Hour)}, true, "2026-10-10T15:00:00Z"},
		"refused by the provider":           {coreauth.Auth{Metadata: expiresIn(3 * time.Hour), LastError: &coreauth.Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized}}, true, "2026-10-10T15:00:00Z"},
		"refused with invalid_grant":        {coreauth.Auth{Metadata: expiresIn(3 * time.Hour), LastError: &coreauth.Error{Message: "oauth: Invalid_Grant"}}, true, "2026-10-10T15:00:00Z"},
		"the provider could not be reached": {coreauth.Auth{Metadata: expiresIn(3 * time.Hour), LastError: &coreauth.Error{Message: "dial tcp: no route to host"}}, false, ""},
		"already run out":                   {coreauth.Auth{Metadata: expiresIn(-time.Hour)}, true, ""},
		"no expiry, renewed long ago":       {coreauth.Auth{Metadata: renewed(5 * time.Hour)}, true, ""},
		"no expiry, renewed recently":       {coreauth.Auth{Metadata: renewed(4*time.Hour + 10*time.Minute)}, false, ""},
		"no expiry, renewed by the SDK":     {coreauth.Auth{Metadata: renewed(9 * time.Hour), LastRefreshedAt: now.Add(-time.Hour)}, false, ""},
		"nothing to go by":                  {coreauth.Auth{}, false, ""},
		"an API key never renews":           {coreauth.Auth{Metadata: expiresIn(time.Hour), Attributes: map[string]string{"api_key": "k"}}, false, ""},
		"a disabled account":                {coreauth.Auth{Metadata: expiresIn(time.Hour), Disabled: true}, false, ""},
		"a provider with no renewal rule":   {coreauth.Auth{Metadata: expiresIn(time.Hour), Provider: "unknown-provider"}, false, ""},
	} {
		tc.auth.ID = "a"
		if tc.auth.Provider == "" {
			tc.auth.Provider, tc.auth.Runtime = "claude", renewsBefore(4*time.Hour)
		}
		state := accountState(&tc.auth, now)
		ending, _ := state["signInEnding"].(bool)
		until, _ := state["signInEndsAt"].(string)
		if ending != tc.ending || until != tc.until {
			t.Fatalf("%s: ending=%v until=%q, want %v %q", name, ending, until, tc.ending, tc.until)
		}
	}
}

func TestAccountRefreshRenewsASignInAndRefusesAnAPIKey(t *testing.T) {
	body := `{"auth_id":"account","provider":"codex"}`
	healthy := &fakeProvider{name: "codex"}
	if response := accountFixture(t, healthy, &coreauth.Auth{}).send(http.MethodPost, "/ao/account-refresh", control, body); response.Code != http.StatusNoContent || healthy.refreshes != 1 {
		t.Fatalf("refresh: %d after %d renewals", response.Code, healthy.refreshes)
	}
	refused := &fakeProvider{name: "codex", refuse: &coreauth.Error{Code: "unauthorized", Message: "unauthorized", HTTPStatus: 401}}
	if response := accountFixture(t, refused, &coreauth.Auth{}).send(http.MethodPost, "/ao/account-refresh", control, body); response.Code != http.StatusBadGateway {
		t.Fatalf("refused renewal: %d", response.Code)
	}
	key := &fakeProvider{name: "codex"}
	if response := accountFixture(t, key, &coreauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}).send(http.MethodPost, "/ao/account-refresh", control, body); response.Code != http.StatusBadRequest || key.refreshes != 0 {
		t.Fatalf("API key: %d after %d renewals", response.Code, key.refreshes)
	}
}
