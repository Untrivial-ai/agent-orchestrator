package postgres

import "testing"

func TestTerminalExitStatePreservesInterfaceHandoff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode int
		handoff  bool
		state    string
		message  string
	}{
		{name: "clean exit", state: "closed"},
		{name: "failed exit", exitCode: 1, state: "failed", message: "Terminal process exited with status 1."},
		{name: "handoff ignores nonzero exit", exitCode: -1, handoff: true, state: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, message := terminalExitState(tc.exitCode, tc.handoff)
			if state != tc.state || message != tc.message {
				t.Fatalf("state/message = %q/%q, want %q/%q", state, message, tc.state, tc.message)
			}
		})
	}
}

func TestAgentTerminalExitVerdictWaitsForInterfaceHandoff(t *testing.T) {
	for _, tc := range []struct {
		name              string
		terminated        bool
		activityExited    bool
		terminalClosed    bool
		handoffInProgress bool
		want              bool
	}{
		{name: "closed terminal during handoff is temporary", terminalClosed: true, handoffInProgress: true},
		{name: "closed terminal without handoff has exited", terminalClosed: true, want: true},
		{name: "terminated session stays ended during handoff", terminated: true, handoffInProgress: true, want: true},
		{name: "exited agent stays ended during handoff", activityExited: true, handoffInProgress: true, want: true},
		{name: "no terminal yet is provisioning", handoffInProgress: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentTerminalExited(tc.terminated, tc.activityExited, tc.terminalClosed, tc.handoffInProgress)
			if got != tc.want {
				t.Fatalf("exit verdict = %t, want %t", got, tc.want)
			}
		})
	}
}

// The read-only-session and viewer-role guards in CreateWorkspaceRequest /
// createWorkerRequest gate file mutations by kind. The review file-write path
// dispatches "workspace.review.write", so both write kinds must be recognized or
// a viewer / read-only member could overwrite files through the review endpoint.
func TestIsWorkspaceWriteKind(t *testing.T) {
	for _, kind := range []string{"workspace.write", "workspace.review.write"} {
		if !isWorkspaceWriteKind(kind) {
			t.Errorf("isWorkspaceWriteKind(%q) = false, want true (must be gated by viewer/read-only checks)", kind)
		}
	}
	for _, kind := range []string{
		"workspace.read", "workspace.list", "workspace.diff", "workspace.diff-file",
		"workspace.review", "workspace.review.file", "terminal.open", "browser.fetch",
	} {
		if isWorkspaceWriteKind(kind) {
			t.Errorf("isWorkspaceWriteKind(%q) = true, want false", kind)
		}
	}
}
