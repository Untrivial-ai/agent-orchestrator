package devin

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusAuthorizedFromDocumentedAPIKey(t *testing.T) {
	t.Setenv("DEVIN_API_KEY", "cog_test")
	got, err := (&Plugin{resolvedBinary: "devin"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}
