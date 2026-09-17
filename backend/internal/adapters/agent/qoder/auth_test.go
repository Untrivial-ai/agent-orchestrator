package qoder

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestTokenPresenceDoesNotClaimAuthorization(t *testing.T) {
	t.Setenv("QODER_PERSONAL_ACCESS_TOKEN", "configured-not-validated")
	p := New(); p.resolvedBinary = "/bin/qoder"
	status, err := p.AuthStatus(context.Background())
	if err != nil { t.Fatal(err) }
	if status != ports.AgentAuthStatusUnknown { t.Fatalf("status = %q, want unknown", status) }
}

func TestVersionGate(t *testing.T) {
	for _, tc := range []struct { value string; ok bool }{{"1.1.54", true}, {"qoder 1.2.0", true}, {"1.1.53", false}, {"unknown", false}} {
		if err := validateVersionOutput(tc.value); (err == nil) != tc.ok { t.Errorf("validateVersionOutput(%q) = %v", tc.value, err) }
	}
}
