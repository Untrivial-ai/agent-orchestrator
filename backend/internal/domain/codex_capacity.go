package domain

import "time"

// CodexCapacityState is the display-safe subscription-capacity classification
// for one Codex account. Capacity is advisory and never participates in launch
// admission.
type CodexCapacityState string

// Codex capacity states classify the provider-reported overall bucket.
const (
	CodexCapacityAvailable   CodexCapacityState = "available"
	CodexCapacityNearLimit   CodexCapacityState = "near_limit"
	CodexCapacityExhausted   CodexCapacityState = "exhausted"
	CodexCapacityUnknown     CodexCapacityState = "unknown"
	CodexCapacityUnsupported CodexCapacityState = "unsupported"
)

// CodexCapacityReachedState is the normalized provider exhaustion signal for a
// single bucket. Unknown is required for sparse notifications that omit it.
type CodexCapacityReachedState string

// Codex capacity reached states normalize explicit provider exhaustion signals.
const (
	CodexCapacityNotReached   CodexCapacityReachedState = "not_reached"
	CodexCapacityReached      CodexCapacityReachedState = "reached"
	CodexCapacityReachUnknown CodexCapacityReachedState = "unknown"
)

// CodexCapacityWindow is one safe provider rate-limit window.
type CodexCapacityWindow struct {
	UsedPercent           float64    `json:"usedPercent" minimum:"0" maximum:"100"`
	WindowDurationMinutes *int64     `json:"windowDurationMinutes,omitempty"`
	ResetsAt              *time.Time `json:"resetsAt,omitempty"`
}

// CodexCapacityBucket is one provider meter with stable, display-safe fields.
type CodexCapacityBucket struct {
	LimitID     string                    `json:"limitId"`
	DisplayName *string                   `json:"displayName,omitempty"`
	Primary     *CodexCapacityWindow      `json:"primary,omitempty"`
	Secondary   *CodexCapacityWindow      `json:"secondary,omitempty"`
	Reached     CodexCapacityReachedState `json:"reached" enum:"not_reached,reached,unknown"`
}

// CodexResetCreditsSummary is the safe subset of provider-reported usage-limit
// reset credits. Opaque credit identifiers and raw provider detail rows remain
// private to the Codex app-server process.
type CodexResetCreditsSummary struct {
	AvailableCount   int64      `json:"availableCount" minimum:"0"`
	NearestExpiresAt *time.Time `json:"nearestExpiresAt,omitempty"`
}

// CodexCapacitySnapshot is the daemon-memory capacity observation exposed on a
// account. Raw provider payloads and opaque reset-credit identifiers are
// deliberately absent.
type CodexCapacitySnapshot struct {
	State             CodexCapacityState        `json:"state" enum:"available,near_limit,exhausted,unknown,unsupported"`
	Freshness         AgentReadinessFreshness   `json:"freshness" enum:"fresh,stale,checking"`
	Plan              *string                   `json:"plan,omitempty"`
	UsedPercent       *float64                  `json:"usedPercent,omitempty" minimum:"0" maximum:"100"`
	RemainingPercent  *float64                  `json:"remainingPercent,omitempty" minimum:"0" maximum:"100"`
	ResetsAt          *time.Time                `json:"resetsAt,omitempty"`
	ObservedAt        *time.Time                `json:"observedAt,omitempty"`
	CheckedAt         *time.Time                `json:"checkedAt,omitempty"`
	AttemptedAt       *time.Time                `json:"attemptedAt,omitempty"`
	ReasonCode        string                    `json:"reasonCode"`
	Reason            string                    `json:"reason"`
	Overall           *CodexCapacityBucket      `json:"overall,omitempty"`
	AdditionalBuckets []CodexCapacityBucket     `json:"additionalBuckets"`
	ResetCredits      *CodexResetCreditsSummary `json:"resetCredits,omitempty"`
}

// CodexCapacitySummary is the compact session-list projection of the current
// account snapshot. It intentionally excludes buckets and account identity.
type CodexCapacitySummary struct {
	State            CodexCapacityState      `json:"state" enum:"available,near_limit,exhausted,unknown,unsupported"`
	Freshness        AgentReadinessFreshness `json:"freshness" enum:"fresh,stale,checking"`
	Plan             *string                 `json:"plan,omitempty"`
	UsedPercent      *float64                `json:"usedPercent,omitempty" minimum:"0" maximum:"100"`
	RemainingPercent *float64                `json:"remainingPercent,omitempty" minimum:"0" maximum:"100"`
	ResetsAt         *time.Time              `json:"resetsAt,omitempty"`
	ObservedAt       *time.Time              `json:"observedAt,omitempty"`
	ReasonCode       string                  `json:"reasonCode"`
	Reason           string                  `json:"reason"`
}

// Codex capacity reason codes are stable, display-safe explanations.
const (
	CodexCapacityReasonNotChecked         = "capacity_not_checked"
	CodexCapacityReasonChecking           = "capacity_checking"
	CodexCapacityReasonAvailable          = "capacity_available"
	CodexCapacityReasonNearLimit          = "capacity_near_limit"
	CodexCapacityReasonExhausted          = "capacity_exhausted"
	CodexCapacityReasonUnsupported        = "capacity_unsupported"
	CodexCapacityReasonSkippedSignedOut   = "capacity_skipped_signed_out"
	CodexCapacityReasonSkippedAuthUnknown = "capacity_skipped_auth_unknown"
	CodexCapacityReasonAccountUnavailable = "capacity_account_unavailable"
	CodexCapacityReasonInvalidated        = "capacity_invalidated"
	CodexCapacityReasonCheckInconclusive  = "capacity_check_inconclusive"
	CodexCapacityReasonCheckTimeout       = "capacity_check_timeout"
	CodexCapacityReasonCheckFailed        = "capacity_check_failed"
)

// CompactCodexCapacity removes bucket details for ordinary session reads.
func CompactCodexCapacity(snapshot CodexCapacitySnapshot) CodexCapacitySummary {
	return CodexCapacitySummary{
		State: snapshot.State, Freshness: snapshot.Freshness, Plan: snapshot.Plan,
		UsedPercent: snapshot.UsedPercent, RemainingPercent: snapshot.RemainingPercent, ResetsAt: snapshot.ResetsAt,
		ObservedAt: snapshot.ObservedAt, ReasonCode: snapshot.ReasonCode, Reason: snapshot.Reason,
	}
}
