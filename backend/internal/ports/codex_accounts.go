package ports

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CodexCapacityObservation is the rate-limit data a live Codex conversation emits.
type CodexCapacityObservation struct {
	Plan              *string
	Overall           *domain.CodexCapacityBucket
	AdditionalBuckets []domain.CodexCapacityBucket
	ResetCredits      *domain.CodexResetCreditsSummary
	ObservedAt        time.Time
	Partial           bool
}
