package aider

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
		{name: "response ready", event: "notification", want: domain.ActivityWaitingInput, wantOK: true},
		{name: "unknown event", event: "session-start"},
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
