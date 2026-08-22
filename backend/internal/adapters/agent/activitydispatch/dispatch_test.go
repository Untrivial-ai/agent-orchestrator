package activitydispatch

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Every deriver key must be a known harness name except fake, whose deriver is
// retained for test fixtures and historical callbacks even though the harness is
// no longer user-selectable. SupportsHarness equates tokens and harnesses, so any
// other drift would silently report a hooked harness as hook-less.
func TestDeriverTokensAreKnownHarnesses(t *testing.T) {
	for token := range Derivers {
		if token == string(domain.HarnessFake) {
			continue
		}
		if !domain.AgentHarness(token).IsKnown() {
			t.Errorf("deriver token %q is not a known AgentHarness", token)
		}
	}
}

func TestSupportsHarness(t *testing.T) {
	for _, h := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode, domain.HarnessGrok, domain.HarnessContinue, domain.HarnessMuse, domain.HarnessOpenCode, domain.HarnessKimi, domain.HarnessVibe, domain.HarnessPrimeAgent} {
		if !SupportsHarness(h) {
			t.Errorf("SupportsHarness(%q) = false, want true", h)
		}
	}
	// Harnesses whose adapters install no hooks must read as unsupported so
	// their silence never derives no_signal.
	for _, h := range []domain.AgentHarness{domain.HarnessAmp, domain.HarnessAider, domain.HarnessCrush, domain.AgentHarness("")} {
		if SupportsHarness(h) {
			t.Errorf("SupportsHarness(%q) = true, want false", h)
		}
	}
}

func TestPrimeAgentDerivesManagedExtensionActivity(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    domain.ActivityState
		wantOK  bool
	}{
		{"promptless startup", "session-start", `{"reason":"startup"}`, domain.ActivityIdle, true},
		{"prompt submit", "user-prompt-submit", `{"prompt":"fix it"}`, domain.ActivityActive, true},
		{"agent end", "stop", `{}`, domain.ActivityIdle, true},
		{"quit", "session-end", `{"reason":"quit"}`, domain.ActivityExited, true},
		{"internal reset", "session-end", `{"reason":"reload"}`, "", false},
		{"malformed shutdown", "session-end", `{`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Derive("prime-agent", tt.event, []byte(tt.payload))
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("Derive(prime-agent, %q) = (%q, %v), want (%q, %v)", tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestMuseDerivesManagedHookActivity(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
	}{
		{"user-prompt-submit", domain.ActivityActive},
		{"permission-request", domain.ActivityBlocked},
		{"stop", domain.ActivityIdle},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got, ok := Derive("muse", tt.event, []byte(`{}`))
			if !ok || got != tt.want {
				t.Fatalf("Derive(muse, %q) = (%q, %v), want (%q, true)", tt.event, got, ok, tt.want)
			}
		})
	}
	if got, ok := Derive("muse", "session-start", []byte(`{}`)); ok {
		t.Fatalf("Derive(muse, session-start) = (%q, true), want metadata-only", got)
	}
}

func TestGrokDerivesClaudeCompatibleActivity(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    domain.ActivityState
	}{
		{"permission request", "permission-request", `{}`, domain.ActivityBlocked},
		{"idle notification", "notification", `{"notification_type":"idle_prompt"}`, domain.ActivityIdle},
		{"session end", "session-end", `{}`, domain.ActivityExited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Derive("grok", tt.event, []byte(tt.payload))
			if !ok {
				t.Fatalf("Derive(grok, %q) ok=false, want true", tt.event)
			}
			if got != tt.want {
				t.Fatalf("Derive(grok, %q) = %q, want %q", tt.event, got, tt.want)
			}
		})
	}
}

// Continue delegates GetAgentHooks to the claude-code plugin, so its deriver
// must be claudecode.DeriveActivityState (same as grok's) and behave
// identically.
func TestContinueDerivesClaudeCompatibleActivity(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    domain.ActivityState
	}{
		{"permission request", "permission-request", `{}`, domain.ActivityBlocked},
		{"idle notification", "notification", `{"notification_type":"idle_prompt"}`, domain.ActivityIdle},
		{"session end", "session-end", `{}`, domain.ActivityExited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Derive("continue", tt.event, []byte(tt.payload))
			if !ok {
				t.Fatalf("Derive(continue, %q) ok=false, want true", tt.event)
			}
			if got != tt.want {
				t.Fatalf("Derive(continue, %q) = %q, want %q", tt.event, got, tt.want)
			}
		})
	}
}
