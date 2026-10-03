package opencodev2

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
		{"native session startup", "session-start", domain.ActivityActive, true},
		{"active work", "active", domain.ActivityActive, true},
		{"permission blocked", "permission-blocked", domain.ActivityBlocked, true},
		{"permission resolved", "permission-resolved", domain.ActivityActive, true},
		{"settled turn", "stop", domain.ActivityIdle, true},
		{"unknown event", "unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, nil)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)", tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
