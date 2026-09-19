package registry

import (
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Every shipped chat harness must have an explicit outage-behavior entry, and no
// entry may name a harness that is not shipped. This keeps #4613's "recorded as
// tested, upstream-blocked, or unavailable, never silently claimed as supported"
// true by construction: adding a driver to Build without classifying it fails here.
func TestNetworkResilienceMatrixCoversEveryRegisteredHarness(t *testing.T) {
	shipped := Build(slog.New(slog.DiscardHandler)).Harnesses()

	for _, harness := range shipped {
		entry, ok := NetworkResilienceFor(harness)
		if !ok {
			t.Errorf("harness %q is registered but has no network-resilience entry (#4613)", harness)
			continue
		}
		switch entry.Status {
		case NetworkResilienceHandled, NetworkResilienceUpstreamBlocked, NetworkResilienceUnverified:
		default:
			t.Errorf("harness %q has an unknown network-resilience status %q", harness, entry.Status)
		}
		if entry.Note == "" {
			t.Errorf("harness %q has an empty network-resilience note", harness)
		}
	}

	shippedSet := make(map[domain.AgentHarness]struct{}, len(shipped))
	for _, harness := range shipped {
		shippedSet[harness] = struct{}{}
	}
	for harness := range networkResilienceByHarness {
		if _, ok := shippedSet[harness]; !ok {
			t.Errorf("network-resilience entry %q names a harness with no registered driver", harness)
		}
	}
}
