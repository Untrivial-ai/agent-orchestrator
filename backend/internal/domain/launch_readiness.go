package domain

import "time"

// LaunchReadinessState is a durable fact about one launch, independent of activity.
type LaunchReadinessState string

// Launch readiness states preserve startup evidence after the process exits.
const (
	LaunchReadinessLaunching     LaunchReadinessState = "launching"
	LaunchReadinessReady         LaunchReadinessState = "ready"
	LaunchReadinessNeedsInput    LaunchReadinessState = "needs_input"
	LaunchReadinessLaunchFailed  LaunchReadinessState = "launch_failed"
	LaunchReadinessResumeInvalid LaunchReadinessState = "resume_invalid"
)

// LaunchReadiness binds startup evidence to its process and conversation.
// An empty state preserves the behavior of sessions created before tracking.
type LaunchReadiness struct {
	State          LaunchReadinessState `json:"state" enum:",launching,ready,needs_input,launch_failed,resume_invalid"`
	Cause          string               `json:"cause,omitempty"`
	UpdatedAt      time.Time            `json:"updatedAt"`
	LaunchID       string               `json:"-"`
	ConversationID string               `json:"-"`
	Resume         bool                 `json:"-"`
}

// Unresolved reports whether this launch is still waiting for readiness evidence.
func (r LaunchReadiness) Unresolved() bool {
	return r.State == LaunchReadinessLaunching || r.State == LaunchReadinessNeedsInput
}

// LaunchFailureCause carries an explicit diagnosis, never an inference from an exit code.
type LaunchFailureCause string

// Supported diagnoses come from observed startup failures.
const (
	LaunchFailureResumeInvalid      LaunchFailureCause = "resume_invalid"
	LaunchFailureProcessStartFailed LaunchFailureCause = "process_start_failed"
)

// Valid accepts an absent diagnosis for older reporters.
func (c LaunchFailureCause) Valid() bool {
	return c == "" || c == LaunchFailureResumeInvalid || c == LaunchFailureProcessStartFailed
}
