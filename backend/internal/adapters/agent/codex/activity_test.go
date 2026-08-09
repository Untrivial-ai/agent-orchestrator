package codex

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestEmitsBlockedActivity(t *testing.T) {
	if New().EmitsBlockedActivity() {
		t.Fatal("Codex must remain ineligible for automated Enter confirmation")
	}
}

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"user prompt clears stale wait -> active", "user-prompt-submit", domain.ActivityActive, true},
		{"request user input start -> blocked", "pre-tool-use", domain.ActivityBlocked, true},
		{"request user input completion -> active", "post-tool-use", domain.ActivityActive, true},
		{"permission request -> waiting_input", "permission-request", domain.ActivityWaitingInput, true},
		{"turn stop clears interrupted wait -> idle", "stop", domain.ActivityIdle, true},
		{"session start -> no signal", "session-start", "", false},
		{"unknown event -> no signal", "frobnicate", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, []byte(`{}`))
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)",
					tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
