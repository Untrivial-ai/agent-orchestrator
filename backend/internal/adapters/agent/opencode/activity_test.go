package opencode

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"session start -> active", "session-start", domain.ActivityActive, true},
		{"user prompt -> active", "user-prompt-submit", domain.ActivityActive, true},
		{"tool or reply -> active", "active", domain.ActivityActive, true},
		{"stop -> idle", "stop", domain.ActivityIdle, true},
		{"permission or question -> blocked", "permission-blocked", domain.ActivityBlocked, true},
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
