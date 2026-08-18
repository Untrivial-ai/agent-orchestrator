package activitystate

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStandardDeriveActivityState(t *testing.T) {
	cases := []struct {
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"session-start", domain.ActivityActive, true},
		{"user-prompt-submit", domain.ActivityActive, true},
		{"tool-use", domain.ActivityActive, true},
		{"stop", domain.ActivityIdle, true},
		{"permission-request", domain.ActivityWaitingInput, true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := StandardDeriveActivityState(tc.event, []byte("ignored"))
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("event %q: got (%q, %v), want (%q, %v)", tc.event, got, ok, tc.want, tc.wantOK)
		}
	}
}
