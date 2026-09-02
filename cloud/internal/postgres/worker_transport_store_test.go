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
