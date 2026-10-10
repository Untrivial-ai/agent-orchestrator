package host

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// providerOrigins is where the calls made as a provider's account may go.
var providerOrigins = map[string]string{"codex": "https://chatgpt.com/", "claude": "https://api.anthropic.com/"}

func providerCall(ctx context.Context, m *coreauth.Manager, auth *coreauth.Auth, method, url string, body *json.RawMessage) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	headers := http.Header{"Accept": {"application/json"}}
	if auth.Provider == "claude" {
		// Anthropic rejects subscription tokens without this header.
		headers.Set("anthropic-beta", "oauth-2025-04-20")
	}
	var payload []byte
	if body != nil {
		payload = *body
		headers.Set("Content-Type", "application/json")
	}
	req, err := m.NewHttpRequest(ctx, auth, method, url, payload, headers)
	if err != nil {
		return 0, nil, err
	}
	resp, err := m.HttpRequest(ctx, auth, req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}
func claudeEmail(ctx context.Context, m *coreauth.Manager, auth *coreauth.Auth) string {
	var profile struct{ Account map[string]any }
	status, data, err := providerCall(ctx, m, auth, http.MethodGet, providerOrigins["claude"]+"api/oauth/profile", nil)
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &profile) != nil {
		return ""
	}
	return cmp.Or(text(profile.Account["email"]), text(profile.Account["email_address"]))
}

// accountState is what only the helper knows about an account, under the names of AO's usage type.
func accountState(auth *coreauth.Auth, now time.Time) gin.H {
	requests := []gin.H{}
	for _, slice := range auth.RecentRequestsSnapshot(now) {
		requests = append(requests, gin.H{"succeeded": slice.Success, "failed": slice.Failed})
	}
	out := gin.H{"requests": requests}
	set := func(key string, at time.Time) {
		if !at.IsZero() {
			out[key] = at.UTC().Format(time.RFC3339)
		}
	}
	set("addedAt", auth.CreatedAt)
	set("refreshedAt", cmp.Or(auth.LastRefreshedAt, instant(auth.Metadata["last_refresh"])))
	if until, reason := accountPause(auth, now); until.After(now) {
		set("pausedUntil", until)
		out["pausedReason"] = reason
	}
	if ending, until := signInEnding(auth, now); ending {
		out["signInEnding"] = true
		if until.After(now) {
			set("signInEndsAt", until)
		}
	}
	// A Codex sign-in's saved token names the day its plan ends.
	plan, _ := jwtClaims(text(auth.Metadata["id_token"]))["https://api.openai.com/auth"].(map[string]any)
	set("renewsAt", instant(plan["chatgpt_subscription_active_until"]))
	return out
}
func accountPause(auth *coreauth.Auth, now time.Time) (until time.Time, reason string) {
	for _, view := range coreauth.CooldownSnapshotForAuth(auth, now) {
		switch {
		// A sign-in failure or an unsupported model is not a pause.
		case slices.Contains([]string{"model_not_supported", "unauthorized", "invalid_grant"}, view.Reason):
		case view.Scope == "credential":
			return view.RetryAt, view.Reason
		case view.RetryAt.After(until):
			until, reason = view.RetryAt, view.Reason
		}
	}
	return until, reason
}

// signInEnding reports a sign-in that still works but is overdue for renewal, and when its token runs out.
func signInEnding(auth *coreauth.Auth, now time.Time) (bool, time.Time) {
	// The SDK renews within seconds of the due moment and retries every five minutes: half an hour late is a refusal.
	const renewalGrace = 30 * time.Minute
	lead := coreauth.ProviderRefreshLead(auth.Provider, auth.Runtime)
	if auth.Disabled || auth.Attributes["api_key"] != "" || lead == nil || *lead <= renewalGrace {
		return false, time.Time{}
	}
	// A failure that is not a refusal, such as the provider being out of reach, may pass by itself.
	if e := auth.LastError; e != nil && !slices.Contains([]int{400, 401, 403}, e.HTTPStatus) && !strings.EqualFold(e.Code, "unauthorized") && !strings.Contains(strings.ToLower(e.Message), "invalid_grant") {
		return false, time.Time{}
	}
	if expiry, known := auth.ExpirationTime(); known && !expiry.IsZero() {
		return expiry.Sub(now) <= *lead-renewalGrace, expiry
	}
	// With no expiry to go by, the SDK renews one lead after the last renewal.
	renewed := cmp.Or(auth.LastRefreshedAt, instant(auth.Metadata["last_refresh"]))
	return !renewed.IsZero() && now.Sub(renewed) >= *lead+renewalGrace, time.Time{}
}
func instant(value any) time.Time {
	if seconds, ok := value.(float64); ok && seconds > 0 {
		return time.Unix(int64(seconds), 0)
	}
	at, _ := time.Parse(time.RFC3339, text(value))
	return at
}
