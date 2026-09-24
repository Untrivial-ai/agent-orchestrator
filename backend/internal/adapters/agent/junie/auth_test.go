package junie

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusIsUnknownAfterBinaryResolution(t *testing.T) {
	t.Setenv("JUNIE_API_KEY", "must-not-imply-authorization")
	plugin := &Plugin{resolvedBinary: "/usr/local/bin/junie"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus() = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := (&Plugin{resolvedBinary: "/usr/local/bin/junie"}).AuthStatus(ctx)
	if status != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("AuthStatus() = (%q, %v), want (%q, context canceled)", status, err, ports.AgentAuthStatusUnknown)
	}
}
