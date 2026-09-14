// Package switchengine owns the pure agent-switch saga policy shared by the
// TUI and Chat executors (session_manager/agent_switching.go and
// agent_switching_chat.go).
//
// Both executors run the same phases — admit → stop-source → start-target →
// deliver → settle — and make the same settlement decisions in their deferred
// cleanup. The decisions depend only on small durable facts, so they live here
// as pure functions with table tests. The executors keep the mode-specific
// store calls (ActivateAgentSwitchTarget vs ActivateChatAgentSwitchTarget,
// runtime launch id vs controller generation); they must not reimplement the
// conditions below.
//
// Full executor unification behind a targetStarter interface is the intended
// follow-up; Outcome is the decision seam it will be built on.
package switchengine

import "time"

// Outcome captures the settlement facts both executors track as locals.
// Failed reports whether the executor is returning an error; StateTerminal
// and RequiresRecovery mirror the durable switch record at settle time.
type Outcome struct {
	Failed              bool
	SourceStopped       bool
	OwnerCommitted      bool
	TargetAmbiguous     bool
	WorkspacePrepared   bool
	StateTerminal       bool
	RequiresRecovery    bool
	SkipTerminalization bool
}

// RollbackSafe reports whether the source may be restored: the switch failed
// after a conclusive source stop, ownership never transferred, and no
// ambiguous target side effect blocks rollback.
func (o Outcome) RollbackSafe() bool {
	return o.Failed && o.SourceStopped && !o.OwnerCommitted && !o.TargetAmbiguous
}

// NeedsWorkspaceCleanup reports whether prepared target workspace state must
// be removed before any source rollback.
func (o Outcome) NeedsWorkspaceCleanup() bool {
	return o.Failed && o.WorkspacePrepared && !o.OwnerCommitted && !o.TargetAmbiguous
}

// NeedsRetainedMarker reports whether a non-terminal switch must persist a
// recovery marker instead of a terminal failure.
func (o Outcome) NeedsRetainedMarker() bool {
	return o.Failed && !o.StateTerminal && o.SkipTerminalization && !o.RequiresRecovery
}

// NeedsTerminalFailure reports whether a non-terminal switch must persist a
// terminal failure.
func (o Outcome) NeedsTerminalFailure() bool {
	return o.Failed && !o.StateTerminal && !o.SkipTerminalization
}

// NeedsDeferredWorkspaceCleanup reports whether prepared workspace state must
// be removed after settlement when ownership never transferred.
func (o Outcome) NeedsDeferredWorkspaceCleanup() bool {
	return o.WorkspacePrepared && !o.OwnerCommitted && !o.TargetAmbiguous
}

// ResolvePostStopWait returns the configured post-stop budget, falling back to
// the package default when unset. Both executors resolve it identically after
// the conclusive source-stop boundary.
func ResolvePostStopWait(configured, fallback time.Duration) time.Duration {
	if configured <= 0 {
		return fallback
	}
	return configured
}
