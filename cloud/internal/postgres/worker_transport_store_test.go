package postgres

import "testing"

func TestTerminalExitStatePreservesInterfaceHandoff(t *testing.T) {
	tests := []struct {
		name             string
		exitCode         int
		interfaceHandoff bool
		wantState        string
		wantMessage      string
	}{
		{name: "clean process exit", exitCode: 0, wantState: "closed"},
		{name: "failed process exit", exitCode: 1, wantState: "failed", wantMessage: "Terminal process exited with status 1."},
		{name: "handoff with nonzero process exit", exitCode: -1, interfaceHandoff: true, wantState: "closed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, message := terminalExitState(test.exitCode, test.interfaceHandoff)
			if state != test.wantState || message != test.wantMessage {
				t.Fatalf("terminalExitState(%d, %t) = (%q, %q), want (%q, %q)",
					test.exitCode, test.interfaceHandoff, state, message, test.wantState, test.wantMessage)
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
