package domain

import (
	"math"
	"strings"
	"time"
)

// QuotaProviderID is the stable, provider-neutral identity of a quota source.
// Values are deliberately opaque to the service and UI: adding a provider must
// not require widening an enum throughout AO.
type QuotaProviderID string

// QuotaAccountID is a local, non-secret provider account identity. Adapters use
// "default" when their protocol does not expose a stable account identifier.
type QuotaAccountID string

// QuotaLimitID is the provider's stable bucket identity. Providers may add new
// buckets without an AO release, so callers must not switch exhaustively on it.
type QuotaLimitID string

type QuotaCompleteness string

const (
	QuotaComplete QuotaCompleteness = "complete"
	QuotaPartial  QuotaCompleteness = "partial"
)

type QuotaLimitCategory string

const (
	QuotaRateLimit    QuotaLimitCategory = "rate_limit"
	QuotaSpendLimit   QuotaLimitCategory = "spend_limit"
	QuotaUsageCredits QuotaLimitCategory = "usage_credits"
	QuotaResetCredits QuotaLimitCategory = "reset_credits"
)

type QuotaLimitScope string

const (
	QuotaAccountScope   QuotaLimitScope = "account"
	QuotaWorkspaceScope QuotaLimitScope = "workspace"
	QuotaModelScope     QuotaLimitScope = "model"
)

// QuotaCapabilities describe what an adapter can truthfully provide. The UI
// renders from these flags instead of branching on provider names.
type QuotaCapabilities struct {
	SupportsRead        bool `json:"supportsRead"`
	SupportsSubscribe   bool `json:"supportsSubscribe"`
	SupportsHistory     bool `json:"supportsHistory"`
	SupportsCredits     bool `json:"supportsCredits"`
	SupportsSpendLimits bool `json:"supportsSpendLimits"`
}

// QuotaSnapshot is one provider account's quota position. A snapshot may be
// partial (Claude ACP pushes one bucket at a time) or complete (Codex read).
type QuotaSnapshot struct {
	Provider     QuotaProviderID
	AccountID    QuotaAccountID
	AccountLabel string
	PlanType     string
	AuthMode     string
	Capabilities QuotaCapabilities
	Limits       []QuotaLimit
	Balances     []QuotaBalance
	ObservedAt   time.Time
	Completeness QuotaCompleteness
	RefreshError string
}

// QuotaLimit is one independently enforced provider bucket. Nil values mean the
// provider did not report the field; they must never be rendered as zero.
type QuotaLimit struct {
	ID             QuotaLimitID
	Name           string
	Category       QuotaLimitCategory
	Scope          QuotaLimitScope
	ScopeID        string
	UsedPercent    *float64
	RemainingValue *float64
	TotalValue     *float64
	Unit           string
	WindowType     string
	WindowDuration *time.Duration
	ResetsAt       *time.Time
	Reached        *bool
	ReachedReason  string
	ObservedAt     time.Time
}

// RemainingPercent derives capacity only when the provider reported a used
// percentage. It does not attempt to translate tokens into prompts or tasks.
func (l QuotaLimit) RemainingPercent() *float64 {
	if l.UsedPercent == nil {
		return nil
	}
	remaining := math.Max(0, math.Min(100, 100-*l.UsedPercent))
	return &remaining
}

// QuotaBalance is a provider-reported credit or monetary balance. Value stays a
// string so decimal precision is not lost and non-monetary credits still fit.
type QuotaBalance struct {
	ID         string
	Name       string
	Value      string
	Currency   string
	Unlimited  bool
	ObservedAt time.Time
}

type QuotaHistoryPoint struct {
	LimitID     QuotaLimitID
	WindowType  string
	Scope       QuotaLimitScope
	ScopeID     string
	UsedPercent *float64
	ResetsAt    *time.Time
	Reached     *bool
	ObservedAt  time.Time
}

type QuotaAlert struct {
	ID        string
	Provider  QuotaProviderID
	AccountID QuotaAccountID
	LimitID   QuotaLimitID
	Kind      string
	Severity  string
	Title     string
	Body      string
	CreatedAt time.Time
}

// NormalizeQuotaSnapshot applies safe defaults at the provider-neutral boundary.
func NormalizeQuotaSnapshot(snapshot QuotaSnapshot) QuotaSnapshot {
	if strings.TrimSpace(string(snapshot.AccountID)) == "" {
		snapshot.AccountID = "default"
	}
	if snapshot.Completeness == "" {
		snapshot.Completeness = QuotaPartial
	}
	for i := range snapshot.Limits {
		limit := &snapshot.Limits[i]
		if limit.Category == "" {
			limit.Category = QuotaRateLimit
		}
		if limit.Scope == "" {
			limit.Scope = QuotaAccountScope
		}
		if limit.ObservedAt.IsZero() {
			limit.ObservedAt = snapshot.ObservedAt
		}
	}
	for i := range snapshot.Balances {
		if snapshot.Balances[i].ObservedAt.IsZero() {
			snapshot.Balances[i].ObservedAt = snapshot.ObservedAt
		}
	}
	return snapshot
}
