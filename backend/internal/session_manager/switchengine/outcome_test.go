package switchengine

import (
	"testing"
	"time"
)

func TestOutcomeRollbackSafe(t *testing.T) {
	base := Outcome{Failed: true, SourceStopped: true}
	if !base.RollbackSafe() {
		t.Fatal("failed post-stop switch with no owner should roll back")
	}
	for name, mutate := range map[string]func(*Outcome){
		"success":          func(o *Outcome) { o.Failed = false },
		"source live":      func(o *Outcome) { o.SourceStopped = false },
		"owner committed":  func(o *Outcome) { o.OwnerCommitted = true },
		"target ambiguous": func(o *Outcome) { o.TargetAmbiguous = true },
	} {
		o := base
		mutate(&o)
		if o.RollbackSafe() {
			t.Fatalf("%s: RollbackSafe = true, want false", name)
		}
	}
}

func TestOutcomeSettlementBranches(t *testing.T) {
	failed := Outcome{Failed: true}
	if !failed.NeedsTerminalFailure() {
		t.Fatal("plain failed switch must persist a terminal failure")
	}
	if failed.NeedsRetainedMarker() {
		t.Fatal("non-skipped switch must not persist a retained marker")
	}
	retained := Outcome{Failed: true, SkipTerminalization: true}
	if !retained.NeedsRetainedMarker() {
		t.Fatal("failed skipped switch should persist a retained marker")
	}
	if retained.NeedsTerminalFailure() {
		t.Fatal("skipped switch must not also persist a terminal failure")
	}
	terminal := Outcome{Failed: true, StateTerminal: true, SkipTerminalization: true}
	if terminal.NeedsRetainedMarker() || terminal.NeedsTerminalFailure() {
		t.Fatal("terminal switch settles nothing further")
	}
	recovering := Outcome{Failed: true, SkipTerminalization: true, RequiresRecovery: true}
	if recovering.NeedsRetainedMarker() {
		t.Fatal("switch requiring recovery must not write a second marker")
	}
	cleanup := Outcome{Failed: true, WorkspacePrepared: true}
	if !cleanup.NeedsWorkspaceCleanup() || !cleanup.NeedsDeferredWorkspaceCleanup() {
		t.Fatal("prepared uncommitted workspace must be cleaned")
	}
	committed := Outcome{Failed: true, WorkspacePrepared: true, OwnerCommitted: true}
	if committed.NeedsWorkspaceCleanup() || committed.NeedsDeferredWorkspaceCleanup() {
		t.Fatal("committed target owns its workspace; no cleanup")
	}
}

func TestResolvePostStopWait(t *testing.T) {
	if got := ResolvePostStopWait(0, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("zero configured = %s, want fallback", got)
	}
	if got := ResolvePostStopWait(-time.Second, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("negative configured = %s, want fallback", got)
	}
	if got := ResolvePostStopWait(250*time.Millisecond, 2*time.Minute); got != 250*time.Millisecond {
		t.Fatalf("configured = %s, want configured", got)
	}
}
