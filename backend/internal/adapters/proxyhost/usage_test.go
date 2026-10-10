package proxyhost

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	usageNow      = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	codexAccount  = domain.ProviderAccount{Provider: "codex", Kind: "oauth", AuthID: "auth-1", CredentialRef: "codex.json"}
	claudeAccount = domain.ProviderAccount{Provider: "claude", Kind: "oauth", AuthID: "auth-1", CredentialRef: "claude.json"}
)

func upstream(status int, body string) string {
	return fmt.Sprintf(`{"status":%d,"body":%s}`, status, body)
}

// providerHelper answers account-state with state and each provider call with
// the answer listed for its "METHOD url"; a call not listed finds nothing.
func providerHelper(t *testing.T, state string, answers map[string]string) func(call) (int, string) {
	return func(received call) (int, string) {
		var sent accountCall
		if err := json.Unmarshal([]byte(received.Body), &sent); err != nil || sent.AuthID != "auth-1" {
			t.Errorf("%s for another account: %v", received.line(), err)
		}
		switch received.Path {
		case "/ao/account-state":
			return http.StatusOK, state
		case "/ao/provider-call":
			if answer, ok := answers[sent.Method+" "+sent.URL]; ok {
				return http.StatusOK, answer
			}
			return http.StatusOK, upstream(http.StatusNotFound, `"not found"`)
		}
		return http.StatusNoContent, ``
	}
}

func decoded(t *testing.T, document string) (value any) {
	t.Helper()
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func count(value int64) *int64 { return &value }

func TestCodexUsageReadsEveryLimitResetAndTheTokenTally(t *testing.T) {
	state := `{"addedAt":"2029-09-03T08:00:00Z","refreshedAt":"2030-01-01T11:48:00Z","pausedUntil":"2030-01-01T12:48:00Z","pausedReason":"quota",
		"signInEnding":true,"signInEndsAt":"2030-01-01T15:00:00Z","renewsAt":"2030-11-14T00:00:00Z","requests":[{"succeeded":9,"failed":0},{"succeeded":4,"failed":3}]}`
	c, helper := helperClient(t, providerHelper(t, state, map[string]string{
		"GET https://chatgpt.com/backend-api/wham/usage": upstream(200, `{"plan_type":"pro",
			"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":1893502800},"secondary_window":{"used_percent":25,"limit_window_seconds":604800}},
			"code_review_rate_limit":{"primary_window":{"used_percent":50,"limit_window_seconds":604800}},
			"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000}}}],
			"credits":{"has_credits":true,"unlimited":false,"balance":"12.50"},"rate_limit_reset_credits":{"available_count":3}}`),
		"GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits": upstream(200, `{"available_count":3,"applicable_available_count":1,"credits":[
			{"status":"available","reset_type":"codex_rate_limits","title":"Later","expires_at":"2030-02-04T00:00:00Z"},
			{"status":"available","reset_type":"codex_rate_limits","title":"Sooner","expires_at":1895702400},
			{"status":"available","reset_type":"codex_rate_limits","title":"Open"},
			{"status":"redeemed","reset_type":"codex_rate_limits","expires_at":"2030-01-02T00:00:00Z"}]}`),
		"GET https://chatgpt.com/backend-api/wham/profiles/me": upstream(200, `{"name":"A Person","stats":{"lifetime_tokens":142300000000,"peak_daily_tokens":7000000000,
			"longest_running_turn_sec":158760.5,"current_streak_days":6,"longest_streak_days":23,
			"daily_usage_buckets":[{"start_date":"2030-01-01","tokens":1240000},{"start_date":"2029-12-31","tokens":5}]}}`),
	}))
	usage, err := c.AccountUsage(ctx, codexAccount)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ProviderAccountUsage{
		Status: "available", Plan: "pro",
		// The two general limits first, then every scoped one.
		Windows: []domain.ProviderAccountUsageWindow{
			{DurationSeconds: 18000, RemainingFraction: 0, ResetTime: "2030-01-01T13:00:00Z"},
			{DurationSeconds: 604800, RemainingFraction: 0.75},
			{Scope: domain.ProviderUsageScopeCodeReview, DurationSeconds: 604800, RemainingFraction: 0.5},
			{Scope: domain.ProviderUsageScopeModel, Name: "GPT-5.3-Codex-Spark", DurationSeconds: 18000, RemainingFraction: 1},
		},
		// Soonest to expire first; a spent reset is not listed.
		ResetCredits: count(3), ResetUsable: true, Resets: []domain.ProviderAccountReset{
			{Label: "Sooner", Left: 1, Total: 1, ExpiresAt: "2030-01-27T00:00:00Z"}, {Label: "Later", Left: 1, Total: 1, ExpiresAt: "2030-02-04T00:00:00Z"}, {Label: "Open", Left: 1, Total: 1},
		},
		Credits: &domain.ProviderAccountCredits{Balance: "12.50"},
		// What the helper knows arrives under the usage type's own names.
		AddedAt: "2029-09-03T08:00:00Z", RefreshedAt: "2030-01-01T11:48:00Z", PausedUntil: "2030-01-01T12:48:00Z", PausedReason: "quota",
		SignInEnding: true, SignInEndsAt: "2030-01-01T15:00:00Z", RenewsAt: "2030-11-14T00:00:00Z",
		Requests: []domain.ProviderAccountRequests{{Succeeded: 9}, {Succeeded: 4, Failed: 3}},
		Tokens:   &domain.ProviderAccountTokens{LatestDay: "2030-01-01", LatestDayTokens: count(1240000), Lifetime: count(142300000000), PeakDaily: count(7000000000), LongestTurnSeconds: count(158760), CurrentStreakDays: count(6), LongestStreakDays: count(23)},
	}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("usage=%+v\n tokens=%+v", usage, usage.Tokens)
	}
	for _, sent := range helper.calls {
		if sent.Path == "/ao/provider-call" && decoded(t, sent.Body).(map[string]any)["method"] != "GET" {
			t.Fatalf("usage was read with %s", sent.Body)
		}
	}
}

func TestUsageSurvivesAReshapedFieldAndFailsWithoutAReading(t *testing.T) {
	answers := map[string]string{"GET https://chatgpt.com/backend-api/wham/usage": upstream(200, `{"plan_type":"plus",
		"rate_limit":{"primary_window":{"used_percent":"30"},"secondary_window":{"used_percent":50,"limit_window_seconds":604800}},
		"additional_rate_limits":{"GPT-5.3-Codex-Spark":{"primary_window":{"used_percent":0}}},"credits":"none",
		"rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`)}
	c, _ := helperClient(t, providerHelper(t, `{}`, answers))
	usage, err := c.AccountUsage(ctx, codexAccount)
	// The provider's own word on whether a reset applies wins over the reached limit.
	if err != nil || usage.Status != "available" || len(usage.Windows) != 1 || usage.Credits != nil || usage.Tokens != nil || *usage.ResetCredits != 1 || usage.ResetUsable {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	answers["GET https://chatgpt.com/backend-api/wham/usage"] = upstream(500, `"upstream error"`)
	if _, err = c.AccountUsage(ctx, codexAccount); err == nil {
		t.Fatal("a failed reading was reported as usage")
	}
	// Providers report limits for subscriptions only: a key shows what the helper knows.
	key := domain.ProviderAccount{Provider: "codex", Kind: "api_key", AuthID: "auth-1", CredentialRef: "config-index:codex:1"}
	c, helper := helperClient(t, providerHelper(t, `{"addedAt":"2029-09-03T08:00:00Z"}`, nil))
	usage, err = c.AccountUsage(ctx, key)
	if want := (domain.ProviderAccountUsage{Status: "unavailable", AddedAt: "2029-09-03T08:00:00Z"}); err != nil || !reflect.DeepEqual(usage, want) || !reflect.DeepEqual(helper.lines(), []string{"POST /ao/account-state"}) {
		t.Fatalf("usage=%+v err=%v calls=%v", usage, err, helper.lines())
	}
}

func TestClaudeUsageReadsScopedLimitsPlanAndResetGrants(t *testing.T) {
	c, _ := helperClient(t, providerHelper(t, `{}`, map[string]string{
		"GET https://api.anthropic.com/api/oauth/usage": upstream(200, `{
			"five_hour":{"utilization":100,"resets_at":"2030-01-01T14:15:00Z"},"seven_day":{"utilization":50,"resets_at":"2030-01-05T00:00:00Z"},
			"seven_day_opus":{"utilization":75,"resets_at":"2030-01-05T00:00:00Z"},"seven_day_sonnet":null,"seven_day_cowork":{"utilization":null,"resets_at":null},
			"seven_day_oauth_apps":{"utilization":0},
			"limits":[{"kind":"weekly_scoped","percent":25,"scope":{"model":{"display_name":"opus"}}},{"kind":"weekly_scoped","percent":25,"resets_at":"2030-01-06T00:00:00Z","scope":{"model":{"display_name":"Fable"}}},
				{"kind":"session","percent":5,"scope":{"model":{"display_name":"Haiku"}}}],
			"extra_usage":{"is_enabled":true,"monthly_limit":5000,"used_credits":1820.4,"utilization":36.4}}`),
		"GET https://api.anthropic.com/api/oauth/profile": upstream(200, `{"account":{"email":"alice@example.test","has_claude_max":true,"has_claude_pro":false},
			"organization":{"uuid":"0F8FAD5B-D9CB-469F-A165-70867728950E","name":"alice@example.test's Organization","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`),
		"GET https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1": upstream(200, `{"cedar_ember":{"eligible":true,"at_limit":true,"grants":[
			{"id":"g1","label":"Launch bonus","resets_total":3,"resets_left":2,"ends_at":"2099-01-30T00:00:00Z","usable_now":true},
			{"id":"g2","resets_total":1,"resets_left":0,"usable_now":true},
			{"id":"g3","resets_total":1,"resets_left":1,"ends_at":"2020-12-30T00:00:00Z","usable_now":true}]}}`),
	}))
	usage, err := c.AccountUsage(ctx, claudeAccount)
	if err != nil {
		t.Fatal(err)
	}
	week := "2030-01-05T00:00:00Z"
	model := domain.ProviderUsageScopeModel
	want := domain.ProviderAccountUsage{
		Status: "available", Plan: "max", PlanTier: "20x",
		// Opus is listed once, under its named field; limits without a figure are left out.
		Windows: []domain.ProviderAccountUsageWindow{
			{DurationSeconds: 18000, RemainingFraction: 0, ResetTime: "2030-01-01T14:15:00Z"}, {DurationSeconds: 604800, RemainingFraction: 0.5, ResetTime: week},
			{Scope: model, Name: "Opus", DurationSeconds: 604800, RemainingFraction: 0.25, ResetTime: week}, {Scope: model, Name: "Fable", DurationSeconds: 604800, RemainingFraction: 0.75, ResetTime: "2030-01-06T00:00:00Z"},
			{Scope: domain.ProviderUsageScopeApps, DurationSeconds: 604800, RemainingFraction: 1},
		},
		// A grant that is spent or has ended is not listed; a personal organization is not named.
		ResetCredits: count(2), ResetUsable: true, Resets: []domain.ProviderAccountReset{{Label: "Launch bonus", Left: 2, Total: 3, ExpiresAt: "2099-01-30T00:00:00Z"}},
		ExtraUsage: &domain.ProviderAccountExtraUsage{UsedCents: 1820, LimitCents: 5000},
	}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("usage=%+v", usage)
	}
	grant := `{"id":"g1","resets_total":1,"resets_left":1,"usable_now":true`
	for name, tc := range map[string]struct {
		grants, profile string
		want            domain.ProviderAccountUsage
	}{
		"below the limit":         {grants: `{"eligible":true,"grants":[` + grant + `}]}`, want: domain.ProviderAccountUsage{ResetCredits: count(1), Resets: []domain.ProviderAccountReset{{Left: 1, Total: 1}}}},
		"usable below the limit":  {grants: `{"eligible":true,"grants":[` + grant + `,"use_requires_limit":false}]}`, want: domain.ProviderAccountUsage{ResetCredits: count(1), ResetUsable: true, Resets: []domain.ProviderAccountReset{{Left: 1, Total: 1}}}},
		"paused":                  {grants: `{"eligible":true,"at_limit":true,"grants":[` + grant + `,"paused":true}]}`, want: domain.ProviderAccountUsage{ResetCredits: count(1), Resets: []domain.ProviderAccountReset{{Left: 1, Total: 1}}}},
		"not started":             {grants: `{"eligible":true,"at_limit":true,"grants":[` + grant + `,"starts_at":"2030-01-02T00:00:00Z"}]}`, want: domain.ProviderAccountUsage{ResetCredits: count(1), Resets: []domain.ProviderAccountReset{{Left: 1, Total: 1}}}},
		"cooling down":            {grants: `{"eligible":true,"at_limit":true,"cooldown_until":"2030-01-01T16:00:00Z","grants":[` + grant + `}]}`, want: domain.ProviderAccountUsage{ResetCredits: count(1), ResetBlockedUntil: "2030-01-01T16:00:00Z", Resets: []domain.ProviderAccountReset{{Left: 1, Total: 1}}}},
		"not eligible":            {grants: `{"eligible":false,"at_limit":true,"grants":[` + grant + `}]}`},
		"an active team":          {grants: `null`, profile: `{"account":{"has_claude_max":true},"organization":{"name":"Example Team","organization_type":"claude_team","subscription_status":"active"}}`, want: domain.ProviderAccountUsage{Plan: "team", Organization: "Example Team"}},
		"a free personal account": {grants: `null`, profile: `{"account":{"has_claude_max":false,"has_claude_pro":false},"organization":{"name":"Solo","organization_type":"claude_free"}}`, want: domain.ProviderAccountUsage{Plan: "free"}},
	} {
		got := domain.ProviderAccountUsage{}
		claudeUsage(&got, decoded(t, `{"five_hour":{"utilization":100}}`), decoded(t, `{"cedar_ember":`+tc.grants+`}`), decoded(t, cmp.Or(tc.profile, `null`)), usageNow)
		got.Windows = nil
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: usage=%+v", name, got)
		}
	}
}

func TestResetSpendsOneResetAndNamesTheProvidersAnswer(t *testing.T) {
	const consume = "POST https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	const claim = "POST https://api.anthropic.com/api/organizations/0f8fad5b-d9cb-469f-a165-70867728950e/reset_rate_limits"
	profile := upstream(200, `{"organization":{"uuid":"0F8FAD5B-D9CB-469F-A165-70867728950E"}}`)
	grants := func(status string) string {
		return upstream(200, `{"cedar_ember":{`+status+`"grants":[{"id":"g3","resets_left":1,"usable_now":true},{"id":"g2","resets_left":1,"usable_now":true},{"id":"g1","resets_left":1,"usable_now":false}]}}`)
	}
	for name, tc := range map[string]struct {
		account          domain.ProviderAccount
		profile, grants  string
		answer           string
		want, wantClaim  string
		resumed, refused bool
	}{
		"Codex: reset":               {account: codexAccount, answer: upstream(200, `{"code":"reset"}`), want: domain.ProviderResetDone, wantClaim: `{"redeem_request_id":"req-1"}`, resumed: true},
		"Codex: the same claim":      {account: codexAccount, answer: upstream(200, `{"code":"already_redeemed"}`), want: domain.ProviderResetDone, wantClaim: `{"redeem_request_id":"req-1"}`, resumed: true},
		"Codex: nothing to reset":    {account: codexAccount, answer: upstream(200, `{"code":"nothing_to_reset"}`), want: domain.ProviderResetNothing, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Codex: none left":           {account: codexAccount, answer: upstream(200, `{"code":"no_credit"}`), want: domain.ProviderResetNone, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Codex: an unknown answer":   {account: codexAccount, answer: upstream(200, `"<html>"`), want: domain.ProviderResetUnknown, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Codex: too many requests":   {account: codexAccount, answer: upstream(429, `{}`), want: domain.ProviderResetWait, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Codex: refused":             {account: codexAccount, answer: upstream(403, `{}`), want: domain.ProviderResetFailed, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Codex: provider error":      {account: codexAccount, answer: upstream(502, `{}`), want: domain.ProviderResetUnknown, wantClaim: `{"redeem_request_id":"req-1"}`},
		"Claude: the first grant":    {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,`), answer: upstream(200, `{"result":"reset"}`), want: domain.ProviderResetDone, wantClaim: `{"grant_id":"g2","program":"cedar_ember","request_id":"req-1"}`, resumed: true},
		"Claude: the grant named":    {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,"next_grant_id":"g3",`), answer: upstream(200, `{"result":"already_used"}`), want: domain.ProviderResetDone, wantClaim: `{"grant_id":"g3","program":"cedar_ember","request_id":"req-1"}`, resumed: true},
		"Claude: an unusable name":   {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,"next_grant_id":"g1",`), answer: upstream(200, `{"result":"not_limited"}`), want: domain.ProviderResetNothing, wantClaim: `{"grant_id":"g2","program":"cedar_ember","request_id":"req-1"}`},
		"Claude: refused":            {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,`), answer: upstream(403, `{}`), want: domain.ProviderResetFailed, wantClaim: `{"grant_id":"g2","program":"cedar_ember","request_id":"req-1"}`},
		"Claude: an unclear refusal": {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,`), answer: upstream(409, `{}`), want: domain.ProviderResetUnknown, wantClaim: `{"grant_id":"g2","program":"cedar_ember","request_id":"req-1"}`},
		"Claude: below the limit":    {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,`), want: domain.ProviderResetNothing},
		"Claude: not eligible":       {account: claudeAccount, profile: profile, grants: grants(`"at_limit":true,`), want: domain.ProviderResetNone},
		"Claude: cooling down":       {account: claudeAccount, profile: profile, grants: grants(`"eligible":true,"at_limit":true,"cooldown_until":"2099-01-01T00:00:00Z",`), want: domain.ProviderResetWait},
		"Claude: no grants read":     {account: claudeAccount, profile: profile, grants: upstream(500, `{}`), want: domain.ProviderResetFailed},
		"Claude: no organization":    {account: claudeAccount, profile: upstream(200, `{}`), grants: grants(`"eligible":true,"at_limit":true,`), want: domain.ProviderResetFailed},
		"an account the helper lost": {account: codexAccount, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			answers := map[string]string{"GET " + claudeProfileURL: tc.profile, "GET " + claudeGrantsURL: tc.grants, consume: tc.answer, claim: tc.answer}
			answer := providerHelper(t, `{}`, answers)
			c, helper := helperClient(t, func(received call) (int, string) {
				if tc.refused && received.Path == "/ao/provider-call" {
					return http.StatusNotFound, `{}`
				}
				return answer(received)
			})
			outcome, err := c.AccountAction(ctx, tc.account, ports.AccountActionReset, "req-1")
			if tc.refused {
				if !errors.Is(err, ports.ErrProviderAccountActionUnavailable) || outcome != "" {
					t.Fatalf("outcome=%q err=%v", outcome, err)
				}
				return
			}
			claimed, resumed := "", false
			for _, sent := range helper.calls {
				if body, _ := decoded(t, sent.Body).(map[string]any); body["method"] == "POST" {
					data, _ := json.Marshal(body["body"])
					claimed = string(data)
				}
				resumed = resumed || sent.Path == "/ao/account-resume"
			}
			if err != nil || outcome != tc.want || claimed != tc.wantClaim || resumed != tc.resumed {
				t.Fatalf("outcome=%q err=%v claim=%s resumed=%v calls=%v", outcome, err, claimed, resumed, helper.lines())
			}
		})
	}
}

func TestAccountActionsThatAreNotResets(t *testing.T) {
	status := http.StatusNoContent
	c, helper := helperClient(t, func(call) (int, string) { return status, `` })
	key := domain.ProviderAccount{Provider: "claude", Kind: "api_key", AuthID: "auth-1"}
	for _, unavailable := range [][2]string{{ports.AccountActionReset, "api key"}, {"rename", ""}} {
		account := codexAccount
		if unavailable[1] != "" {
			account = key
		}
		if _, err := c.AccountAction(ctx, account, unavailable[0], "req-1"); !errors.Is(err, ports.ErrProviderAccountActionUnavailable) || len(helper.calls) != 0 {
			t.Fatalf("%v: err=%v calls=%v", unavailable, err, helper.lines())
		}
	}
	for action, path := range map[string]string{ports.AccountActionResume: "/ao/account-resume", ports.AccountActionRefresh: "/ao/account-refresh"} {
		helper.calls, status = nil, http.StatusNoContent
		outcome, err := c.AccountAction(ctx, claudeAccount, action, "")
		if sent := helper.calls[0]; err != nil || outcome != "" || sent.line() != "POST "+path || sent.Body != `{"auth_id":"auth-1","provider":"claude"}` {
			t.Fatalf("%s: err=%v sent=%+v", action, err, sent)
		}
		// The helper has no sign-in to renew for a key, or no longer has the account.
		for _, refused := range []int{http.StatusBadRequest, http.StatusNotFound} {
			status = refused
			if _, err = c.AccountAction(ctx, key, action, ""); !errors.Is(err, ports.ErrProviderAccountActionUnavailable) {
				t.Fatalf("%s refused with %d: err=%v", action, refused, err)
			}
		}
		status = http.StatusBadGateway
		if _, err = c.AccountAction(ctx, claudeAccount, action, ""); err == nil || errors.Is(err, ports.ErrProviderAccountActionUnavailable) {
			t.Fatalf("%s failed upstream: err=%v", action, err)
		}
	}
}

func TestAClaudePlanIsReadFromTheProfileWhenTheUsageReadingIsMissing(t *testing.T) {
	profile := upstream(200, `{"account":{"has_claude_max":true,"has_claude_pro":false},"organization":{"name":"Example Team","organization_type":"claude_enterprise","rate_limit_tier":"default_claude_max_5x"}}`)
	for name, reading := range map[string]string{"no answer": upstream(500, `"upstream error"`), "an answer without limits": upstream(200, `{"extra_usage":{"is_enabled":true}}`)} {
		c, _ := helperClient(t, providerHelper(t, `{"addedAt":"2029-09-03T08:00:00Z"}`, map[string]string{"GET " + claudeAPI + "/api/oauth/usage": reading, "GET " + claudeProfileURL: profile}))
		usage, err := c.AccountUsage(ctx, claudeAccount)
		// A Claude sign-in always has limits, so this is a failed reading that still names the plan.
		want := domain.ProviderAccountUsage{Status: "unavailable", Plan: "max", PlanTier: "5x", Organization: "Example Team", AddedAt: "2029-09-03T08:00:00Z"}
		if err == nil || !reflect.DeepEqual(usage, want) {
			t.Fatalf("%s: usage=%+v err=%v", name, usage, err)
		}
	}
	// Codex names the plan in the usage reading itself: a plan without limits is a reading.
	c, _ := helperClient(t, providerHelper(t, `{}`, map[string]string{"GET " + codexAPI + "/usage": upstream(200, `{"plan_type":"free"}`)}))
	if usage, err := c.AccountUsage(ctx, codexAccount); err != nil || usage.Status != "available" || usage.Plan != "free" || len(usage.Windows) != 0 {
		t.Fatalf("codex: usage=%+v err=%v", usage, err)
	}
}
