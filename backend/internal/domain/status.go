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
	// StatusStarting marks a live session whose current launch has not proven
	// readiness yet: the agent process exists but AO is still waiting for its
	// first hook truth. Rendered instead of a confident Working.
	StatusStarting SessionStatus = "starting"
	// StatusLaunchFailed marks a live session whose current launch ended before
	// proving readiness. The session is not dead (it can be respawned), but
	// AO can prove "Failed to start" rather than guess.
	StatusLaunchFailed SessionStatus = "launch_failed"
	// StatusResumeInvalid marks a live session whose current launch was a
	// resume that could not pick the conversation back up.
	StatusResumeInvalid SessionStatus = "resume_invalid"
	// StatusNoSignal marks a live session whose agent has never delivered a
	// hook callback for the current spawn/restore: AO cannot tell whether the
	// agent is working or stuck (broken hook pipeline, blocked interactive
	// prompt). Rendered instead of a confident idle.
	StatusNoSignal SessionStatus = "no_signal"
)
