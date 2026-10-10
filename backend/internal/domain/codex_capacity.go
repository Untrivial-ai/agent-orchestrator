package domain

import "time"

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
