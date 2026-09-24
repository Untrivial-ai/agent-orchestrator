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
