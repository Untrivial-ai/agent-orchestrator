package domain

// SessionStatus is the single-word DISPLAY status the dashboard renders. It is
// derived from persisted session facts plus PR facts and is never stored.
type SessionStatus string

// The display statuses the dashboard renders.
const (
	StatusWorking          SessionStatus = "working"
	StatusPROpen           SessionStatus = "pr_open"
	StatusDraft            SessionStatus = "draft"
	StatusCIFailed         SessionStatus = "ci_failed"
	StatusReviewPending    SessionStatus = "review_pending"
	StatusChangesRequested SessionStatus = "changes_requested"
	StatusApproved         SessionStatus = "approved"
	StatusMergeable        SessionStatus = "mergeable"
	StatusMerged           SessionStatus = "merged"
	StatusNeedsInput       SessionStatus = "needs_input"
	StatusExited           SessionStatus = "exited"
	StatusIdle             SessionStatus = "idle"
	StatusTerminated       SessionStatus = "terminated"
	// StatusNoSignal marks a live session whose agent has never delivered a
	// hook callback for the current spawn/restore: AO cannot tell whether the
	// agent is working or stuck (broken hook pipeline, blocked interactive
	// prompt). Rendered instead of a confident idle.
	StatusNoSignal SessionStatus = "no_signal"
)
