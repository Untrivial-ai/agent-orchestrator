package domain

import (
	"hash/fnv"
	"strings"
)

// AccountProviders are the providers whose accounts AO manages.
var AccountProviders = []string{"codex", "claude"}

// AccountProvider names the managed provider behind a harness, or "" for none.
func AccountProvider(h AgentHarness) string {
	return map[AgentHarness]string{HarnessCodex: "codex", HarnessClaudeCode: "claude"}[h]
}

// ProviderAccount is one saved account. CredentialRef and AuthID name its
// sign-in inside the account helper and never leave the daemon; both are empty
// once the account is signed out.
type ProviderAccount struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	Kind          string `json:"kind"`
	CredentialRef string `json:"credential_ref"`
	AuthID        string `json:"auth_id"`
	// Reserved keeps the account out of new sessions. OnLimit names the account
	// its sessions move to when a limit is reached, and WarnAt the percent left
	// at which the user is warned; zero is never.
	Reserved bool   `json:"reserved,omitempty"`
	OnLimit  string `json:"on_limit,omitempty"`
	WarnAt   int    `json:"warn_at,omitempty"`
}

// APIKey reports an account that is an API key and not a sign-in.
func (a ProviderAccount) APIKey() bool { return a.Kind == "api_key" }

// SignedIn reports an account that holds a saved sign-in or key.
func (a ProviderAccount) SignedIn() bool { return a.CredentialRef != "" && a.AuthID != "" }

// GeneratedProviderAccountName is the friendly label a new account starts with.
func GeneratedProviderAccountName(provider, id string) string {
	words := []string{"Cedar", "Maple", "Willow", "River", "Summit", "Harbor", "Meadow", "Pine", "Juniper", "Clover", "Ember", "Atlas"}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(provider + ":" + id)))
	return words[int(h.Sum32())%len(words)] + " " + map[string]string{"codex": "Codex", "claude": "Claude"}[provider]
}

// ProviderSessionRoute sends one session's requests to one account. An empty
// AccountID is a session waiting for its provider's first sign-in.
type ProviderSessionRoute struct {
	SessionID SessionID `json:"session_id"`
	Provider  string    `json:"provider"`
	AccountID string    `json:"account_id"`
}

// NativeProviderImport remembers which of this computer's own logins or keys
// was imported, so a later removal is not undone by the next check.
type NativeProviderImport struct {
	Fingerprint string `json:"fingerprint"`
	AccountID   string `json:"account_id"`
	Email       string `json:"email,omitempty"`
}

// ProviderAccountState is everything Account Manager stores. Defaults maps a
// provider to its default account; a provider present with an empty value has
// had every account signed out.
type ProviderAccountState struct {
	Accounts         []ProviderAccount               `json:"accounts"`
	Defaults         map[string]string               `json:"defaults,omitempty"`
	Routes           []ProviderSessionRoute          `json:"routes"`
	NativeImports    map[string]NativeProviderImport `json:"native_imports,omitempty"`
	NativeKeyImports map[string]NativeProviderImport `json:"native_key_imports,omitempty"`
}

// ProviderAccountView is one account as the API shows it.
type ProviderAccountView struct {
	ID          string `json:"id"`
	Provider    string `json:"provider" enum:"codex,claude"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Kind        string `json:"kind,omitempty" enum:"oauth,imported,api_key"`
	Global      bool   `json:"global,omitempty" description:"This computer's own login or API key."`
	SignedIn    bool   `json:"signedIn" description:"False when signed out or when the provider no longer accepts the saved sign-in."`
	Primary     bool   `json:"primary"`
	// Sessions lists the sessions routed to this account.
	Sessions []string              `json:"sessions"`
	Usage    *ProviderAccountUsage `json:"usage,omitempty"`
	Reserved bool                  `json:"reserved,omitempty" description:"Kept out of new sessions."`
	OnLimit  string                `json:"onLimit,omitempty" description:"The account this one's sessions move to when a limit is reached."`
	WarnAt   int                   `json:"warnAt,omitempty" description:"Percent left at which the user is warned. Absent for never."`
	Moved    *ProviderAccountMove  `json:"moved,omitempty" description:"The last time a reached limit moved this account's sessions away."`
}

// ProviderAccountMove is one automatic move of an account's sessions.
type ProviderAccountMove struct {
	To       string `json:"to"`
	At       string `json:"at"`
	Sessions int    `json:"sessions"`
}

// ProviderAccountUsage is what the provider and the account helper report
// about one account. It never carries a credential.
type ProviderAccountUsage struct {
	Status            string                       `json:"status" enum:"available,unavailable"`
	Plan              string                       `json:"plan,omitempty"`
	PlanTier          string                       `json:"planTier,omitempty" description:"The size of the plan where the provider sells several, such as 20x."`
	Windows           []ProviderAccountUsageWindow `json:"windows,omitempty" description:"The two general limits first, then every scoped limit."`
	ResetCredits      *int64                       `json:"resetCredits,omitempty" description:"Unused usage-limit resets, when the provider reports them."`
	Resets            []ProviderAccountReset       `json:"resets,omitempty" description:"Each unused reset, soonest to expire first, when the provider itemizes them."`
	ResetUsable       bool                         `json:"resetUsable,omitempty" description:"True when the provider would accept a reset right now."`
	ResetBlockedUntil string                       `json:"resetBlockedUntil,omitempty"`
	Credits           *ProviderAccountCredits      `json:"credits,omitempty"`
	ExtraUsage        *ProviderAccountExtraUsage   `json:"extraUsage,omitempty" description:"Present only when pay-as-you-go spending is switched on."`
	RenewsAt          string                       `json:"renewsAt,omitempty"`
	Organization      string                       `json:"organization,omitempty"`
	AddedAt           string                       `json:"addedAt,omitempty"`
	RefreshedAt       string                       `json:"refreshedAt,omitempty" description:"When the saved sign-in was last renewed."`
	PausedUntil       string                       `json:"pausedUntil,omitempty" description:"Set while the account helper holds the account back after a provider refusal."`
	PausedReason      string                       `json:"pausedReason,omitempty"`
	SignInEnding      bool                         `json:"signInEnding,omitempty" description:"The saved sign-in still works but has stopped renewing, so it will stop working."`
	SignInEndsAt      string                       `json:"signInEndsAt,omitempty" description:"When a sign-in that has stopped renewing stops working, if known."`
	Requests          []ProviderAccountRequests    `json:"requests,omitempty" description:"Requests in the last twenty ten-minute slices, oldest first."`
	Tokens            *ProviderAccountTokens       `json:"tokens,omitempty"`
	Activity          *ProviderAccountActivity     `json:"activity,omitempty" description:"What the account helper counted passing through this account."`
	Health            *ProviderAccountHealth       `json:"health,omitempty"`
	Models            []string                     `json:"models,omitempty" description:"The models this account can use."`
}

// ProviderAccountActivity is the account helper's own count of an account's tokens.
type ProviderAccountActivity struct {
	Today    int64                     `json:"today"`
	Week     int64                     `json:"week" description:"The last seven days, today included."`
	Total    int64                     `json:"total" description:"Since the helper began counting."`
	Since    string                    `json:"since,omitempty" description:"The first day counted."`
	Days     []ProviderAccountDay      `json:"days,omitempty" description:"The last fourteen days, oldest first."`
	Models   []ProviderAccountModelUse `json:"models,omitempty" description:"The last seven days by model, largest first."`
	Sessions map[string]int64          `json:"sessions,omitempty" description:"Today's tokens by session."`
}

// ProviderAccountDay is one day's tokens.
type ProviderAccountDay struct {
	Date   string `json:"date"`
	Tokens int64  `json:"tokens"`
}

// ProviderAccountModelUse is one model's share of an account's tokens.
type ProviderAccountModelUse struct {
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
}

// ProviderAccountHealth is how an account's recent requests went.
type ProviderAccountHealth struct {
	LastFailure *ProviderAccountFailure `json:"lastFailure,omitempty"`
	Failures    map[string]int64        `json:"failures,omitempty" description:"Failed requests in the last three hours by kind: limit, signIn, server, other."`
	FirstWordMs int64                   `json:"firstWordMs,omitempty" description:"Typical time to the first token over the last hour."`
}

// ProviderAccountFailure is the last request a provider refused.
type ProviderAccountFailure struct {
	Kind   string `json:"kind" enum:"limit,sign-in,server,other"`
	At     string `json:"at"`
	Status int    `json:"status,omitempty"`
}

// ProviderAccountUsageWindow is one limit and how much of it is left.
type ProviderAccountUsageWindow struct {
	Name              string  `json:"name,omitempty" description:"The provider's name for a model-scoped limit."`
	Scope             string  `json:"scope,omitempty" enum:"code_review,model,oauth_apps,cowork" description:"What the limit covers. Absent for the account's general limits."`
	DurationSeconds   int64   `json:"durationSeconds,omitempty" description:"Length of the limit window in seconds, when the provider reports it."`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

// ProviderAccountReset is one unused allowance to clear the usage limits.
type ProviderAccountReset struct {
	Label     string `json:"label,omitempty"`
	Left      int64  `json:"left"`
	Total     int64  `json:"total"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// ProviderAccountCredits is a prepaid balance spent after the plan's limits.
type ProviderAccountCredits struct {
	Balance   string `json:"balance,omitempty"`
	Unlimited bool   `json:"unlimited,omitempty"`
}

// ProviderAccountExtraUsage is pay-as-you-go spending beyond the plan.
type ProviderAccountExtraUsage struct {
	UsedCents  int64 `json:"usedCents"`
	LimitCents int64 `json:"limitCents" description:"The monthly cap in cents; zero when the provider reports none."`
}

// ProviderAccountRequests counts the requests of one ten-minute slice.
type ProviderAccountRequests struct {
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
}

// ProviderAccountTokens is the provider's own tally of an account's token use.
// A nil figure is one the provider did not report.
type ProviderAccountTokens struct {
	LatestDay          string `json:"latestDay,omitempty" description:"The most recent day the provider has counted, as YYYY-MM-DD."`
	LatestDayTokens    *int64 `json:"latestDayTokens,omitempty"`
	Lifetime           *int64 `json:"lifetime,omitempty"`
	PeakDaily          *int64 `json:"peakDaily,omitempty"`
	LongestTurnSeconds *int64 `json:"longestTurnSeconds,omitempty"`
	CurrentStreakDays  *int64 `json:"currentStreakDays,omitempty"`
	LongestStreakDays  *int64 `json:"longestStreakDays,omitempty"`
}

// Scopes of a usage window, and outcomes of using a reset. Only
// ProviderResetDone means a reset was spent.
const (
	ProviderUsageScopeCodeReview = "code_review"
	ProviderUsageScopeModel      = "model"
	ProviderUsageScopeApps       = "oauth_apps"
	ProviderUsageScopeCowork     = "cowork"

	ProviderResetDone    = "reset"
	ProviderResetNothing = "nothing_to_reset"
	ProviderResetNone    = "none_available"
	ProviderResetWait    = "wait"
	ProviderResetFailed  = "failed"
	ProviderResetUnknown = "unknown"
)
