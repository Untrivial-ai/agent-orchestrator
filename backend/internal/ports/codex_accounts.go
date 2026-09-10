package ports

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CodexAccountContext selects the isolated Codex home used by one structured
// account client. AO-owned homes always force Codex's file credential store.
type CodexAccountContext struct {
	Home    string
	Managed bool
}

// CodexAccountObservation is the safe subset of account/read retained by AO.
type CodexAccountObservation struct {
	Authentication domain.AgentAuthenticationState
	Method         domain.CodexAuthMethod
	Email          *string
}

// CodexCapacityObservation is normalized provider data before cache freshness
// and safe display reasons are applied by the daemon-owned coordinator.
type CodexCapacityObservation struct {
	Plan              *string
	Overall           *domain.CodexCapacityBucket
	AdditionalBuckets []domain.CodexCapacityBucket
	ResetCredits      *domain.CodexResetCreditsSummary
	ObservedAt        time.Time
	Partial           bool
}

// CodexUsageObservation is the deliberately small, display-safe subset of
// account/usage/read retained in daemon memory.
type CodexUsageObservation struct {
	LatestDayTokens           *int64
	LatestDayStartDate        *string
	LifetimeTokens            *int64
	PeakDailyTokens           *int64
	LongestRunningTurnSeconds *int64
	CurrentStreakDays         *int64
	LongestStreakDays         *int64
	ObservedAt                time.Time
}

// CodexAccountEventKind identifies a display-safe app-server account notification.
type CodexAccountEventKind string

const (
	// CodexAccountEventUpdated is emitted when the active Codex account changes.
	CodexAccountEventUpdated CodexAccountEventKind = "account_updated"
	// CodexAccountEventCapacityUpdated is a sparse provider rate-limit update.
	CodexAccountEventCapacityUpdated CodexAccountEventKind = "capacity_updated"
)

// CodexAccountEvent is a normalized account notification. Error details are
// intentionally reduced to a boolean so provider output cannot leak to logs or
// API responses.
type CodexAccountEvent struct {
	Kind     CodexAccountEventKind
	Success  bool
	Failed   bool
	Capacity *CodexCapacityObservation
}

// CodexAccountClient owns one app-server process for one account credential home.
type CodexAccountClient interface {
	Read(ctx context.Context, refreshToken bool) (CodexAccountObservation, error)
	ReadCapacity(ctx context.Context) (CodexCapacityObservation, error)
	ReadUsage(ctx context.Context) (CodexUsageObservation, error)
	ConsumeResetCredit(ctx context.Context, idempotencyKey string) (domain.CodexResetCreditOutcome, error)
	Events() <-chan CodexAccountEvent
	Close() error
}

// CodexAccountClientFactory opens structured account clients and detects the
// installed protocol surface without exposing transport details to services.
type CodexAccountClientFactory interface {
	Open(ctx context.Context, account CodexAccountContext) (CodexAccountClient, error)
	Capabilities(ctx context.Context) domain.CodexAccountCapabilities
}
