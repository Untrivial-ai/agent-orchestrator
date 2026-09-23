package domain

import "testing"

func TestSessionModeValid(t *testing.T) {
	for _, tc := range []struct {
		mode SessionMode
		want bool
	}{
		{SessionModeChat, true},
		{SessionModeTUI, true},
		{"", false},
		{"claude-chat", false},
		{"TUI", false},
	} {
		if got := tc.mode.Valid(); got != tc.want {
			t.Errorf("SessionMode(%q).Valid() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// Reads of durable state must always land on a dispatchable mode: rows written
// before this feature have no mode, and a row written by a newer build may carry
// one this build does not know.
func TestNormalizeSessionModeFallsBackToTUI(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   SessionMode
		want SessionMode
	}{
		{"empty legacy row", "", SessionModeTUI},
		{"unknown future mode", "voice", SessionModeTUI},
		{"chat preserved", SessionModeChat, SessionModeChat},
		{"tui preserved", SessionModeTUI, SessionModeTUI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSessionMode(tc.in); got != tc.want {
				t.Errorf("NormalizeSessionMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Requested modes are parsed strictly. Collapsing an unrecognized request to TUI
// would silently put the caller in a native terminal they deliberately disabled.
func TestParseSessionModeRejectsUnknownInsteadOfFallingBack(t *testing.T) {
	if _, err := ParseSessionMode("codex-tui"); err == nil {
		t.Fatal("ParseSessionMode(\"codex-tui\") = nil error, want a rejection")
	}

	// Absent is not invalid: callers apply their own precedence to the zero value.
	mode, err := ParseSessionMode("")
	if err != nil {
		t.Fatalf("ParseSessionMode(\"\") returned error %v, want none", err)
	}
	if mode != "" {
		t.Errorf("ParseSessionMode(\"\") = %q, want the zero value", mode)
	}

	for _, want := range []SessionMode{SessionModeChat, SessionModeTUI} {
		got, err := ParseSessionMode(string(want))
		if err != nil {
			t.Errorf("ParseSessionMode(%q) returned error %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSessionMode(%q) = %q", want, got)
		}
	}
}

func TestDefaultSessionModeIsTUIForSettingsFailureCompatibility(t *testing.T) {
	if DefaultSessionMode != SessionModeTUI {
		t.Fatalf("DefaultSessionMode = %q, want %q so a settings failure keeps terminal spawns working",
			DefaultSessionMode, SessionModeTUI)
	}
}

func TestTurnStateTerminal(t *testing.T) {
	for _, tc := range []struct {
		state TurnState
		want  bool
	}{
		{TurnStateQueued, false},
		{TurnStateRunning, false},
		{TurnStateCompleted, true},
		// A recovered turn is terminal, but its provider outcome was unavailable
		// during history replay. It must not keep the controller busy.
		{TurnStateRecovered, true},
		// Interrupted is terminal but is not a failure: the provider reports it
		// as its own status and AO must not relabel it as an error.
		{TurnStateInterrupted, true},
		{TurnStateFailed, true},
		{TurnStateCancelled, true},
	} {
		if got := tc.state.Terminal(); got != tc.want {
			t.Errorf("TurnState(%q).Terminal() = %v, want %v", tc.state, got, tc.want)
		}
	}
}
