package agy

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
		{"pre invocation -> active", "pre-invocation", domain.ActivityActive, true},
		{"post tool use -> active", "post-tool-use", domain.ActivityActive, true},
		{"stop -> idle", "stop", domain.ActivityIdle, true},
		{"legacy before agent ignored", "before-agent", "", false},
		{"legacy after agent ignored", "after-agent", "", false},
		{"unknown event -> no signal", "unknown", "", false},
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
