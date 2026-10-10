package proxyhost

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	codexAPI         = "https://chatgpt.com/backend-api/wham"
	claudeAPI        = "https://api.anthropic.com"
	claudeProfileURL = claudeAPI + "/api/oauth/profile"
	claudeGrantsURL  = claudeAPI + "/api/oauth/usage?cedar_ember=1&skip_spend=1"
	fiveHours        = 5 * 60 * 60
	week             = 7 * 24 * 60 * 60
	// An answer the account panel can do without is not waited on for long.
	quick = 4 * time.Second
)

// usageURLs are a provider's usage reading, then its resets, then its profile.
var usageURLs = map[string][]string{
	"codex":  {codexAPI + "/usage", codexAPI + "/rate-limit-reset-credits", codexAPI + "/profiles/me"},
	"claude": {claudeAPI + "/api/oauth/usage", claudeGrantsURL, claudeProfileURL},
}

var planTier = regexp.MustCompile(`\d+x$`)

// accountCall is the body of every helper call about one account.
type accountCall struct {
	AuthID   string `json:"auth_id"`
	Provider string `json:"provider"`
	Method   string `json:"method,omitempty"`
	URL      string `json:"url,omitempty"`
	Body     any    `json:"body,omitempty"`
}

func on(a domain.ProviderAccount) accountCall {
	return accountCall{AuthID: a.AuthID, Provider: a.Provider}
}

// provider sends one request to the account's provider under its sign-in.
func (c *Client) provider(ctx context.Context, a domain.ProviderAccount, method, url string, body any) (int, any, error) {
	var answer struct {
		Status int `json:"status"`
		Body   any `json:"body"`
	}
	err := c.call(ctx, http.MethodPost, "/ao/provider-call", accountCall{a.AuthID, a.Provider, method, url, body}, &answer)
	return answer.Status, answer.Body, err
}

// read returns a provider's answer, or nil when it gave none in time.
func (c *Client) read(ctx context.Context, a domain.ProviderAccount, url string, wait time.Duration) any {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	status, body, err := c.provider(ctx, a, http.MethodGet, url, nil)
	if err != nil || status > 299 {
		return nil
	}
	return body
}

// at walks a decoded answer; a step left out or reshaped costs only its own row.
func at(doc any, path ...string) any {
	for _, key := range path {
		object, _ := doc.(map[string]any)
		doc = object[key]
	}
	return doc
}

func text(doc any, path ...string) string {
	value, _ := at(doc, path...).(string)
	return value
}

func number(doc any, path ...string) (float64, bool) {
	value, ok := at(doc, path...).(float64)
	return value, ok
}

// whole is a count the provider reported, or nil when it reported none.
func whole(doc any, path ...string) *int64 {
	value, ok := number(doc, path...)
	if !ok || value < 0 {
		return nil
	}
	count := int64(value)
	return &count
}

// instant normalizes a provider timestamp, given as text or as Unix seconds.
func instant(value any) string {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(text(value)))
	if seconds, ok := value.(float64); ok && seconds > 0 {
		parsed, err = time.Unix(int64(seconds), 0), nil
	}
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func after(value string, now time.Time) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && parsed.After(now)
}

// AccountUsage reads the helper's facts and, for a subscription, the provider's limits.
func (c *Client) AccountUsage(ctx context.Context, a domain.ProviderAccount) (domain.ProviderAccountUsage, error) {
	usage := domain.ProviderAccountUsage{Status: "unavailable"}
	var answers [3]any
	var wg sync.WaitGroup
	if !a.APIKey() {
		for i, url := range usageURLs[a.Provider] {
			wg.Go(func() { answers[i] = c.read(ctx, a, url, []time.Duration{4 * quick, quick, quick}[i]) })
		}
	}
	err := c.call(ctx, http.MethodPost, "/ao/account-state", on(a), &usage)
	wg.Wait()
	if err != nil || a.APIKey() {
		return usage, err
	}
	if a.Provider == "claude" {
		claudeUsage(&usage, answers[0], answers[1], answers[2], time.Now())
	} else {
		codexUsage(&usage, answers[0], answers[1], answers[2], time.Now())
	}
	// A Claude sign-in always has limits: none means its usage reading did not arrive, though its plan may have.
	if len(usage.Windows) == 0 && (usage.Plan == "" || a.Provider == "claude") {
		return usage, errors.New("the provider reported no usage")
	}
	usage.Status = "available"
	sort.SliceStable(usage.Resets, func(i, j int) bool {
		first, second := usage.Resets[i].ExpiresAt, usage.Resets[j].ExpiresAt
		return first != "" && (second == "" || first < second)
	})
	return usage, nil
}

func codexWindows(limit any, scope, name string, now time.Time) (windows []domain.ProviderAccountUsageWindow) {
	for _, key := range []string{"primary_window", "secondary_window"} {
		used, ok := number(limit, key, "used_percent")
		if !ok || used < 0 || used > 100 {
			continue
		}
		reset := ""
		if when, _ := number(limit, key, "reset_at"); when > 0 {
			reset = instant(when)
		} else if wait, _ := number(limit, key, "reset_after_seconds"); wait > 0 {
			reset = now.UTC().Add(time.Duration(wait) * time.Second).Format(time.RFC3339)
		}
		seconds, _ := number(limit, key, "limit_window_seconds")
		windows = append(windows, domain.ProviderAccountUsageWindow{Name: name, Scope: scope, DurationSeconds: int64(seconds), RemainingFraction: 1 - used/100, ResetTime: reset})
	}
	return windows
}

// codexResets records unused resets: usable at a reached limit, unless Codex says how many apply.
func codexResets(u *domain.ProviderAccountUsage, doc any, reached bool) {
	credits, _ := at(doc, "credits").([]any)
	for _, credit := range credits {
		if text(credit, "status") == "available" && text(credit, "reset_type") == "codex_rate_limits" {
			u.Resets = append(u.Resets, domain.ProviderAccountReset{Label: text(credit, "title"), Left: 1, Total: 1, ExpiresAt: instant(at(credit, "expires_at"))})
		}
	}
	if count := whole(doc, "available_count"); count != nil && u.ResetCredits == nil {
		u.ResetCredits, u.ResetUsable = count, *count > 0 && reached
	}
	if count := whole(doc, "applicable_available_count"); count != nil {
		u.ResetUsable = *count > 0
	}
}

func codexUsage(u *domain.ProviderAccountUsage, doc, resets, profile any, now time.Time) {
	u.Plan, u.Windows = strings.TrimSpace(text(doc, "plan_type")), codexWindows(at(doc, "rate_limit"), "", "", now)
	if u.Plan == "" && len(u.Windows) == 0 {
		return
	}
	reached := at(doc, "rate_limit", "limit_reached") == true || slices.ContainsFunc(u.Windows, func(w domain.ProviderAccountUsageWindow) bool { return w.RemainingFraction <= 0 })
	u.Windows = append(u.Windows, codexWindows(at(doc, "code_review_rate_limit"), domain.ProviderUsageScopeCodeReview, "", now)...)
	scoped, _ := at(doc, "additional_rate_limits").([]any)
	for _, limit := range scoped {
		if name := text(limit, "limit_name"); name != "" {
			u.Windows = append(u.Windows, codexWindows(at(limit, "rate_limit"), domain.ProviderUsageScopeModel, name, now)...)
		}
	}
	balance := strings.TrimSpace(text(doc, "credits", "balance"))
	if amount, ok := number(doc, "credits", "balance"); ok {
		balance = strconv.FormatFloat(amount, 'f', -1, 64)
	}
	if amount, err := strconv.ParseFloat(balance, 64); at(doc, "credits", "unlimited") == true {
		u.Credits = &domain.ProviderAccountCredits{Unlimited: true}
	} else if err == nil && amount > 0 {
		u.Credits = &domain.ProviderAccountCredits{Balance: balance}
	}
	codexResets(u, at(doc, "rate_limit_reset_credits"), reached)
	codexResets(u, resets, reached)
	// The profile also names the person; only the token tally is kept.
	stats := at(profile, "stats")
	tokens := domain.ProviderAccountTokens{Lifetime: whole(stats, "lifetime_tokens"), PeakDaily: whole(stats, "peak_daily_tokens"), LongestTurnSeconds: whole(stats, "longest_running_turn_sec"), CurrentStreakDays: whole(stats, "current_streak_days"), LongestStreakDays: whole(stats, "longest_streak_days")}
	days, _ := at(stats, "daily_usage_buckets").([]any)
	for _, day := range days {
		if date, count := text(day, "start_date"), whole(day, "tokens"); date > tokens.LatestDay && count != nil {
			tokens.LatestDay, tokens.LatestDayTokens = date, count
		}
	}
	if tokens != (domain.ProviderAccountTokens{}) {
		u.Tokens = &tokens
	}
}

// unused reports a Claude reset grant that has resets left and has not ended.
func unused(grant any, now time.Time) bool {
	left, _ := number(grant, "resets_left")
	return left > 0 && (text(grant, "ends_at") == "" || after(text(grant, "ends_at"), now))
}

// spendable reports whether the provider would accept this grant now.
func spendable(grant any, atLimit bool, now time.Time) bool {
	return text(grant, "id") != "" && unused(grant, now) && at(grant, "paused") != true && at(grant, "usable_now") == true &&
		(atLimit || at(grant, "use_requires_limit") == false) && !after(text(grant, "starts_at"), now)
}

func claudeUsage(u *domain.ProviderAccountUsage, doc, grants, profile any, now time.Time) {
	add := func(seconds int64, scope, name string, limit any, percent string) {
		if used, ok := number(limit, percent); ok && used >= 0 && used <= 100 {
			u.Windows = append(u.Windows, domain.ProviderAccountUsageWindow{Name: name, Scope: scope, DurationSeconds: seconds, RemainingFraction: 1 - used/100, ResetTime: text(limit, "resets_at")})
		}
	}
	// The plan comes from the profile, so it is known even when the usage reading is not.
	org := at(profile, "organization")
	kind, name := text(org, "organization_type"), text(org, "name")
	isMax, saysMax := at(profile, "account", "has_claude_max").(bool)
	isPro, saysPro := at(profile, "account", "has_claude_pro").(bool)
	switch {
	case kind == "claude_team" && text(org, "subscription_status") == "active":
		u.Plan = "team"
	case isMax:
		u.Plan = "max"
	case isPro:
		u.Plan = "pro"
	case saysMax && saysPro:
		u.Plan = "free"
	}
	u.PlanTier = planTier.FindString(text(org, "rate_limit_tier"))
	// Only a shared organization is named: a personal one carries its owner's email.
	if (kind == "claude_team" || kind == "claude_enterprise") && !strings.Contains(name, "@") {
		u.Organization = name
	}
	add(fiveHours, "", "", at(doc, "five_hour"), "utilization")
	add(week, "", "", at(doc, "seven_day"), "utilization")
	if len(u.Windows) == 0 {
		return
	}
	add(week, domain.ProviderUsageScopeModel, "Opus", at(doc, "seven_day_opus"), "utilization")
	add(week, domain.ProviderUsageScopeModel, "Sonnet", at(doc, "seven_day_sonnet"), "utilization")
	// Newer model limits arrive as a list instead of a named field.
	limits, _ := at(doc, "limits").([]any)
	for _, limit := range limits {
		name := text(limit, "scope", "model", "display_name")
		if name != "" && text(limit, "kind") == "weekly_scoped" && !slices.ContainsFunc(u.Windows, func(w domain.ProviderAccountUsageWindow) bool { return strings.EqualFold(w.Name, name) }) {
			add(week, domain.ProviderUsageScopeModel, name, limit, "percent")
		}
	}
	add(week, domain.ProviderUsageScopeCowork, "", at(doc, "seven_day_cowork"), "utilization")
	add(week, domain.ProviderUsageScopeApps, "", at(doc, "seven_day_oauth_apps"), "utilization")
	if extra := at(doc, "extra_usage"); at(extra, "is_enabled") == true {
		used, _ := number(extra, "used_credits")
		limit, _ := number(extra, "monthly_limit")
		u.ExtraUsage = &domain.ProviderAccountExtraUsage{UsedCents: int64(max(used, 0)), LimitCents: int64(max(limit, 0))}
	}
	status := at(grants, "cedar_ember")
	if at(status, "eligible") != true {
		return
	}
	list, _ := at(status, "grants").([]any)
	u.ResetCredits = new(int64)
	for _, grant := range list {
		if !unused(grant, now) {
			continue
		}
		left, _ := number(grant, "resets_left")
		total, _ := number(grant, "resets_total")
		*u.ResetCredits += int64(left)
		u.ResetUsable = u.ResetUsable || spendable(grant, at(status, "at_limit") == true, now)
		u.Resets = append(u.Resets, domain.ProviderAccountReset{Label: text(grant, "label"), Left: int64(left), Total: int64(total), ExpiresAt: instant(at(grant, "ends_at"))})
	}
	if cooldown := text(status, "cooldown_until"); after(cooldown, now) {
		u.ResetBlockedUntil, u.ResetUsable = instant(cooldown), false
	}
}
