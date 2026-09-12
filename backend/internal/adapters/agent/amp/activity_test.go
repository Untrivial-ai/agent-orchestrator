package amp

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
		{name: "session start is metadata only", event: "session-start", payload: `{"session_id":"T-1"}`},
		{name: "agent start", event: "user-prompt-submit", payload: `{}`, want: domain.ActivityActive, wantOK: true},
		{name: "agent end", event: "stop", payload: `{}`, want: domain.ActivityIdle, wantOK: true},
		{name: "agent end error needs attention", event: "stop", payload: `{"status":"error"}`, want: domain.ActivityWaitingInput, wantOK: true},
		{name: "agent end cancelled is idle", event: "stop", payload: `{"status":"cancelled"}`, want: domain.ActivityIdle, wantOK: true},
		{name: "thread running", event: "thread-state", payload: `{"state":"running"}`, want: domain.ActivityActive, wantOK: true},
		{name: "thread idle", event: "thread-state", payload: `{"state":"idle"}`, want: domain.ActivityIdle, wantOK: true},
		{name: "thread awaiting approval", event: "thread-state", payload: `{"state":"awaiting-approval"}`, want: domain.ActivityWaitingInput, wantOK: true},
		{name: "thread error", event: "thread-state", payload: `{"state":"error"}`, want: domain.ActivityWaitingInput, wantOK: true},
		{name: "unknown thread state", event: "thread-state", payload: `{"state":"paused"}`},
		{name: "malformed payload", event: "thread-state", payload: `{`},
		{name: "unknown event", event: "tool-call", payload: `{}`},
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
