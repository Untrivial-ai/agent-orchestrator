package pi

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{event: "session-start", want: domain.ActivityIdle, wantOK: true},
		{event: "user-prompt-submit", want: domain.ActivityActive, wantOK: true},
		{event: "stop", want: domain.ActivityIdle, wantOK: true},
		{event: "session-end", want: domain.ActivityExited, wantOK: true},
		{event: "turn-start"},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, nil)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)",
					tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
