package domain

import (
	"fmt"
	"time"
)

// AttentionReason is the daemon watchdog's machine-stable verdict for a
// session that needs a person's eyes. It is derived at read time from durable
// facts (activity state/recency plus recorded worker errors) and never
// persisted: the same facts re-derive the same verdict on every list/get.
type AttentionReason string

// Watchdog attention reasons, from explicit pause states through error-class
// stalls to the generic quiet-worker catch-all.
const (
	// AttentionReasonStalled is an active worker with no progress past the
	// stalled threshold, or an unclassified error followed by silence. The
	// detail carries the last-error excerpt when one exists.
	AttentionReasonStalled AttentionReason = "stalled"
	// AttentionReasonQuestionPending is a worker parked in waiting_input past
	// the question threshold: it asked something as chat (or idled at an
	// empty prompt) instead of raising needs_input promptly.
	AttentionReasonQuestionPending AttentionReason = "question_pending"
	// AttentionReasonDecisionPending is a worker parked in blocked past the
	// question threshold: a permission/approval dialog is open. Automation
	// must never inject input here; the detail carries the DECISION NEEDED
	// marker so renderers can shout it.
	AttentionReasonDecisionPending AttentionReason = "decision_pending"
	// AttentionReasonBlockedInfra is N consecutive retryable provider errors
	// with no intervening progress (rate limits, quota, transient 5xx).
	AttentionReasonBlockedInfra AttentionReason = "blocked_infra"
	// AttentionReasonProviderQuota is a persistent quota/rate-limit error
	// (429, spend limit) with no progress since.
	AttentionReasonProviderQuota AttentionReason = "provider_quota"
	// AttentionReasonProviderAuth is a persistent authentication error
	// (401/403, expired credentials) with no progress since.
	AttentionReasonProviderAuth AttentionReason = "provider_auth"
	// AttentionReasonEnvironmentError is a persistent environment error
	// (EACCES, missing binary, unwritable path) with no progress since.
	AttentionReasonEnvironmentError AttentionReason = "environment_error"
	// AttentionReasonVCSConflict is a persistent version-control/workspace
	// error (worktree checked out elsewhere, merge conflict) with no progress
	// since.
	AttentionReasonVCSConflict AttentionReason = "vcs_conflict"
	// AttentionReasonSwitchRecoveryPending is a non-terminal agent switch whose
	// retained ownership boundary needs a person: exactly the three recovery
	// markers (source_stop_unconfirmed, target_start_unconfirmed,
	// source_restore_unconfirmed). It raises immediately rather than after a
	// quiet period, because the saga already holds the session's interaction
	// lock and no input can move until someone resolves it. Healthy mid-switch
	// sagas stay silent, and the verdict clears itself on the read after the
	// row clears. The detail names the harness pair, the error code, and the
	// Restore affordance.
	AttentionReasonSwitchRecoveryPending AttentionReason = "switch_recovery_pending"
)

// DecisionNeededMarker is the human-facing flag embedded in the detail of a
// decision_pending verdict. A blocked session holds an open permission dialog;
// only a person may answer it.
const DecisionNeededMarker = "DECISION NEEDED"

// WorkerErrorSourceChatTurn names the daemon observation point that recorded a
// worker error. v1 records chat provider-turn failures; the column stays so
// later observers (hook-reported provider errors, workspace failures) share one
// log.
const WorkerErrorSourceChatTurn = "chat_turn"

// WorkerErrorEvent is one durable worker-error fact: a bounded provider-error
// excerpt plus when it happened. Raw facts only — classification into
// quota/auth/env/vcs happens at read time so a classifier fix re-labels old
// rows without a backfill.
type WorkerErrorEvent struct {
	SessionID  SessionID
	Source     string
	TurnID     string
	Message    string
	OccurredAt time.Time
}

// WatchdogConfig is the per-project watchdog threshold override block. Every
// field is optional: zero means unset and resolves to the global default, so
// existing project configs keep behaving exactly as before. Durations are
// whole minutes to keep the JSON shape spec-safe.
type WatchdogConfig struct {
	// StalledAfterMinutes is how long an active worker may go without progress
	// before it reads as stalled. Also the quiet period after the latest
	// unrecovered error before a quota/auth/env/vcs/unknown error raises.
	// Default DefaultStalledAfterMinutes.
	StalledAfterMinutes int64 `json:"stalledAfterMinutes,omitempty"`
	// QuestionPendingAfterMinutes is how long a worker may sit in
	// waiting_input (question) or blocked (decision) before it reads as
	// question_pending / decision_pending. Default
	// DefaultQuestionPendingAfterMinutes.
	QuestionPendingAfterMinutes int64 `json:"questionPendingAfterMinutes,omitempty"`
	// ConsecutiveInfraErrors is how many retryable provider errors with no
	// intervening progress raise blocked_infra, even before anything goes
	// quiet. Default DefaultConsecutiveInfraErrors.
	ConsecutiveInfraErrors int64 `json:"consecutiveInfraErrors,omitempty"`
}

// Global watchdog defaults. Stalled workers read as stuck after ten quiet
// minutes; a parked question/decision pings after five; three consecutive
// retryable provider errors with zero progress in between mean the worker is
// wedged on infrastructure rather than thinking.
const (
	DefaultStalledAfterMinutes         = 10
	DefaultQuestionPendingAfterMinutes = 5
	DefaultConsecutiveInfraErrors      = 3
)

// maxWatchdogMinutes caps per-project minute thresholds at one week so a typo
// cannot park the watchdog for centuries (and so minutes->Duration cannot
// overflow).
const maxWatchdogMinutes = 7 * 24 * 60

// maxConsecutiveInfraErrors caps the blocked-infra count at a value no retry
// loop should ever need: beyond it the session is wedged either way.
const maxConsecutiveInfraErrors = 100

// ResolvedWatchdogConfig is a WatchdogConfig with every unset field replaced
// by its global default. This is what the derivation reads.
type ResolvedWatchdogConfig struct {
	StalledAfter           time.Duration
	QuestionPendingAfter   time.Duration
	ConsecutiveInfraErrors int
}

// DefaultWatchdog resolves the zero WatchdogConfig: the global defaults used
// for standalone sessions and projects that configure nothing.
func DefaultWatchdog() ResolvedWatchdogConfig {
	return WatchdogConfig{}.Resolve()
}

// Resolve overlays the global defaults onto c, filling only thresholds the
// project left unset. A set field is always preserved.
func (c WatchdogConfig) Resolve() ResolvedWatchdogConfig {
	out := ResolvedWatchdogConfig{
		StalledAfter:           DefaultStalledAfterMinutes * time.Minute,
		QuestionPendingAfter:   DefaultQuestionPendingAfterMinutes * time.Minute,
		ConsecutiveInfraErrors: DefaultConsecutiveInfraErrors,
	}
	if c.StalledAfterMinutes > 0 {
		out.StalledAfter = time.Duration(c.StalledAfterMinutes) * time.Minute
	}
	if c.QuestionPendingAfterMinutes > 0 {
		out.QuestionPendingAfter = time.Duration(c.QuestionPendingAfterMinutes) * time.Minute
	}
	if c.ConsecutiveInfraErrors > 0 {
		out.ConsecutiveInfraErrors = int(c.ConsecutiveInfraErrors)
	}
	return out
}

// Validate rejects negative thresholds (zero is unset) and absurd magnitudes
// so a bad config is refused when it is set rather than at derivation time.
func (c WatchdogConfig) Validate() error {
	if c.StalledAfterMinutes < 0 {
		return fmt.Errorf("watchdog.stalledAfterMinutes: must not be negative")
	}
	if c.StalledAfterMinutes > maxWatchdogMinutes {
		return fmt.Errorf("watchdog.stalledAfterMinutes: must not exceed %d (one week)", maxWatchdogMinutes)
	}
	if c.QuestionPendingAfterMinutes < 0 {
		return fmt.Errorf("watchdog.questionPendingAfterMinutes: must not be negative")
	}
	if c.QuestionPendingAfterMinutes > maxWatchdogMinutes {
		return fmt.Errorf("watchdog.questionPendingAfterMinutes: must not exceed %d (one week)", maxWatchdogMinutes)
	}
	if c.ConsecutiveInfraErrors < 0 {
		return fmt.Errorf("watchdog.consecutiveInfraErrors: must not be negative")
	}
	if c.ConsecutiveInfraErrors > maxConsecutiveInfraErrors {
		return fmt.Errorf("watchdog.consecutiveInfraErrors: must not exceed %d", maxConsecutiveInfraErrors)
	}
	return nil
}
