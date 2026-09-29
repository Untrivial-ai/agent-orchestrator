package mimocode

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
		ok    bool
	}{
		{event: "session-start", want: domain.ActivityActive, ok: true},
		{event: "user-prompt-submit", want: domain.ActivityActive, ok: true},
		{event: "active", want: domain.ActivityActive, ok: true},
		{event: "permission-blocked", want: domain.ActivityBlocked, ok: true},
		{event: "stop", want: domain.ActivityIdle, ok: true},
		{event: "unknown"},
	}
	for _, tc := range tests {
		got, ok := DeriveActivityState(tc.event, nil)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)", tc.event, got, ok, tc.want, tc.ok)
		}
	}
}
