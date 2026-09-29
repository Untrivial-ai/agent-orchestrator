package codewhale

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusReportsConfiguredFromLocalSource(t *testing.T) {
	p := &Plugin{
		resolvedBinary: "/opt/codewhale",
		runCommand: func(_ context.Context, _ string, _ string, _ map[string]string, args ...string) ([]byte, error) {
			if len(args) == 2 {
				return []byte("active provider: openai (set via config)\n"), nil
			}
			return []byte("provider: openai\nactive source: env\n"), nil
		},
	}
	status, err := p.AuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestAuthStatusMissingSourceIsUnknown(t *testing.T) {
	p := &Plugin{
		resolvedBinary: "/opt/codewhale",
		runCommand: func(_ context.Context, _ string, _ string, _ map[string]string, args ...string) ([]byte, error) {
			if len(args) == 2 {
				return []byte("active provider: deepseek\n"), nil
			}
			return []byte("active source: missing\n"), nil
		},
	}
	status, err := p.AuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestAuthStatusProbeFailureIsInconclusive(t *testing.T) {
	p := &Plugin{
		resolvedBinary: "/opt/codewhale",
		runCommand: func(context.Context, string, string, map[string]string, ...string) ([]byte, error) {
			return nil, errors.New("probe failed")
		},
	}
	status, err := p.AuthStatus(context.Background())
	if err == nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status=%q err=%v", status, err)
	}
}
