package qoder

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestActivityMapping(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
	}{
		{"session-start", domain.ActivityIdle}, {"user-prompt-submit", domain.ActivityActive},
		{"permission-request", domain.ActivityBlocked}, {"pre-tool-use", domain.ActivityActive},
		{"post-tool-use", domain.ActivityActive}, {"post-tool-use-failure", domain.ActivityActive},
		{"stop", domain.ActivityIdle}, {"session-end", domain.ActivityExited},
	}
	for _, tt := range tests {
		got, ok := DeriveActivityState(tt.event, nil)
		if !ok || got != tt.want {
			t.Errorf("%s = %q,%v want %q,true", tt.event, got, ok, tt.want)
		}
	}
	if _, ok := DeriveActivityState("unknown", nil); ok {
		t.Fatal("unknown event mapped")
	}
}
