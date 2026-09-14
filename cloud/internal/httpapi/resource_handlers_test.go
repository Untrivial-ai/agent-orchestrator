package httpapi

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestSessionResponseIncludesSandboxLifecycleContract(t *testing.T) {
	response := toSessionResponse(domain.Session{
		ID: "session-1", SandboxProvider: "coder",
		DesiredState: "paused", ObservedState: "stopped",
	}, nil)
	if response.SandboxProvider != "coder" ||
		response.DesiredState != "paused" ||
		response.ObservedState != "stopped" {
		t.Fatalf("lifecycle response = %+v", response)
	}
}
