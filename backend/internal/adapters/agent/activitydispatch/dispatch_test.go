package activitydispatch

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Every deriver key must be a known harness name. SupportsHarness equates
// tokens and harnesses, so any drift would silently report a hooked harness as
// hook-less.
func TestDeriverTokensAreKnownHarnesses(t *testing.T) {
	for token := range Derivers {
		if !domain.AgentHarness(token).IsKnown() {
			t.Errorf("deriver token %q is not a known AgentHarness", token)
		}
	}
}

func TestSupportsHarness(t *testing.T) {
	if !SupportsHarness(domain.HarnessOpenCode) {
		t.Errorf("SupportsHarness(opencode) = false, want true")
	}
	// Harnesses with no callback pipeline must read as unsupported.
	for _, h := range []domain.AgentHarness{domain.AgentHarness(""), "not-a-harness"} {
		if SupportsHarness(h) {
			t.Errorf("SupportsHarness(%q) = true, want false", h)
		}
	}
}

func TestDeriveOpenCodeActivity(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
	}{
		{"session-start", domain.ActivityActive},
		{"user-prompt-submit", domain.ActivityActive},
		{"active", domain.ActivityActive},
		{"stop", domain.ActivityIdle},
		{"permission-blocked", domain.ActivityBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got, ok := Derive("opencode", tt.event, []byte(`{}`))
			if !ok || got != tt.want {
				t.Fatalf("Derive(opencode, %q) = (%q, %v), want (%q, true)", tt.event, got, ok, tt.want)
			}
		})
	}
	if got, ok := Derive("opencode", "subagent-stop", []byte(`{}`)); ok {
		t.Fatalf("Derive(opencode, subagent-stop) = (%q, true), want no activity from deriver", got)
	}
}

func TestSignalCoverageForHarness(t *testing.T) {
	tests := []struct {
		harness domain.AgentHarness
		want    SignalCoverage
	}{
		{domain.HarnessOpenCode, SignalCoverageComplete},
		{domain.AgentHarness("not-a-harness"), SignalCoverageNone},
	}

	for _, tt := range tests {
		t.Run(string(tt.harness), func(t *testing.T) {
			if got := CoverageForHarness(tt.harness); got != tt.want {
				t.Fatalf("CoverageForHarness(%q) = %v, want %v", tt.harness, got, tt.want)
			}
		})
	}
}

func TestFullySupportsHarnessRequiresCompleteCoverage(t *testing.T) {
	if !FullySupportsHarness(domain.HarnessOpenCode) {
		t.Fatal("FullySupportsHarness(opencode) = false, want true")
	}
	if FullySupportsHarness(domain.AgentHarness("not-a-harness")) {
		t.Fatal("FullySupportsHarness(not-a-harness) = true, want false")
	}
}
