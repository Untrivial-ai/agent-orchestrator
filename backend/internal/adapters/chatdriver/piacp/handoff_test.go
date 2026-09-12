package piacp_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/pi"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPiRemainsChatOnlyWithoutInterfaceHandoff(t *testing.T) {
	var plugin any = pi.New()
	if _, ok := plugin.(ports.AgentInterfaceHandoff); ok {
		t.Fatal("Pi unexpectedly implements TUI/Chat handoff without a verified shared conversation identity")
	}
}
