package auggie

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    domain.ActivityState
		wantOK  bool
	}{
		{name: "session start", event: "session-start", payload: `{}`, want: domain.ActivityActive, wantOK: true},
		{name: "tool begins", event: "pre-tool-use", payload: `{}`, want: domain.ActivityActive, wantOK: true},
		{name: "tool completes", event: "post-tool-use", payload: `{}`, want: domain.ActivityActive, wantOK: true},
		{name: "normal stop", event: "stop", payload: `{"agent_stop_cause":"end_turn"}`, want: domain.ActivityIdle, wantOK: true},
		{name: "interrupted stop", event: "stop", payload: `{"agent_stop_cause":"interrupted"}`, want: domain.ActivityIdle, wantOK: true},
		{name: "error stop", event: "stop", payload: `{"agent_stop_cause":"error"}`, want: domain.ActivityWaitingInput, wantOK: true},
		{name: "iteration limit", event: "stop", payload: `{"agent_stop_cause":"max_iterations"}`, want: domain.ActivityWaitingInput, wantOK: true},
		{name: "stop without cause", event: "stop", payload: `{}`, wantOK: false},
		{name: "malformed stop", event: "stop", payload: `{`, wantOK: false},
		{name: "unknown stop cause", event: "stop", payload: `{"agent_stop_cause":"future_cause"}`, wantOK: false},
		{name: "session end", event: "session-end", payload: `{}`, want: domain.ActivityExited, wantOK: true},
		{name: "unknown event", event: "notification", payload: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, []byte(tt.payload))
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DeriveActivityState(%q, %q) = (%q, %v), want (%q, %v)",
					tt.event, tt.payload, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
