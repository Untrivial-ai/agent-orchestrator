package traeagent_test

import (
	"testing"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTraeAgentRemainsUnregistered(t *testing.T) {
	// "trae-agent" follows the official repository/package name, "trae-cli"
	// follows the executable, and "trae" is already used for an editor handoff.
	// None may silently become an agent capability.
	for _, harness := range []string{"trae-agent", "trae-cli", "trae"} {
		if domain.AgentHarness(harness).IsKnown() {
			t.Fatalf("%s must not be a selectable harness before TUI conformance", harness)
		}
		for _, adapter := range agentregistry.Constructors() {
			if adapter.Manifest().ID == harness {
				t.Fatalf("%s TUI was registered without passing conformance", harness)
			}
		}
		if chatregistry.Build(nil).SupportsChat(domain.AgentHarness(harness)) {
			t.Fatalf("%s Chat was registered without ACP conformance", harness)
		}
		if domain.ReviewerHarness(harness).IsKnown() {
			t.Fatalf("%s must not be a selectable reviewer before reviewer conformance", harness)
		}
		for _, adapter := range reviewer.Constructors() {
			if adapter.Harness() == domain.ReviewerHarness(harness) {
				t.Fatalf("%s reviewer was registered without reviewer conformance", harness)
			}
		}
	}
}
