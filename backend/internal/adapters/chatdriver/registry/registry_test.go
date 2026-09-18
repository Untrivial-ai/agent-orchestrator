package registry

import (
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// What the daemon ships, stated once.
//
// Registration is the whole capability gate — a harness with no driver here cannot
// run chat mode — so the shipped set is a release decision, not an implementation
// detail. OpenCode uses the reusable ACP transport; every other harness is
// deliberately TUI-only.
func TestShippedChatDrivers(t *testing.T) {
	r := Build(nil)

	for _, harness := range []domain.AgentHarness{
		domain.HarnessOpenCode,
	} {
		if !r.SupportsChat(harness) {
			t.Errorf("%s has no chat driver", harness)
		}
		if _, err := r.Driver(harness); err != nil {
			t.Errorf("resolving the %s driver: %v", harness, err)
		}
	}

	// Every other harness stays TUI-only, and asking for chat must be refused with a
	// typed answer rather than quietly producing a terminal session.
	for _, harness := range []domain.AgentHarness{
		domain.AgentHarness("not-a-harness"),
		"definitely-not-an-agent",
	} {
		if _, err := r.Driver(harness); err == nil {
			t.Errorf("%s resolved a chat driver", harness)
		} else if !errors.Is(err, ports.ErrChatUnsupported) {
			t.Errorf("%s refused with %v, want ErrChatUnsupported so callers can branch", harness, err)
		}
	}
}
