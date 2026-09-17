package junie

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusNeverClaimsAuthorizationFromEnvironment(t *testing.T) {
	t.Setenv("JUNIE_API_KEY", "configured-not-verified")
	p := New()
	p.resolvedBinary = "junie"
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got == ports.AgentAuthStatusAuthorized {
		t.Fatal("environment presence is not a provider round-trip")
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status=%q", got)
	}
}
