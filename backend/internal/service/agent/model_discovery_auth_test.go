package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// countingDiscoverer records whether AO actually executed an agent's CLI.
type countingDiscoverer struct{ runs atomic.Int32 }

func (d *countingDiscoverer) Discover(context.Context, ports.AgentModelDiscoveryRequest) (ports.AgentModelCatalog, error) {
	d.runs.Add(1)
	return ports.AgentModelCatalog{AgentID: "kiro", Models: []ports.AgentModelInfo{{ID: "m1", Label: "M1"}}}, nil
}

func (d *countingDiscoverer) CatalogFingerprint(context.Context, ports.AgentModelDiscoveryRequest) string {
	return "fp"
}

func (d *countingDiscoverer) Manual(agentID string) ports.AgentModelCatalog {
	return ports.AgentModelCatalog{AgentID: agentID, Source: "manual"}
}

func authGateService(status ports.AgentAuthStatus, authErr error) (*Service, *countingDiscoverer) {
	discoverer := &countingDiscoverer{}
	stub := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "kiro-cli", nil },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return status, authErr },
	}
	agents := []agentregistry.HarnessAgent{readinessHarness("kiro", "Kiro", stub)}
	return newService(agents, nil, nil, discoverer), discoverer
}

// Discovery executes the agent's own CLI, and Kiro's discovery command is its
// chat entrypoint — `chat --list-models` — which starts a browser OAuth sign-in
// when it finds no token. A signed-out agent must therefore never reach
// Discover, or rendering a model picker can log the user in.
// https://github.com/Untrivial-ai/agent-orchestrator/issues/5307
func TestSignedOutAgentNeverRunsItsDiscoveryCommand(t *testing.T) {
	svc, discoverer := authGateService(ports.AgentAuthStatusUnauthorized, nil)

	catalog, err := svc.Models(context.Background(), "kiro", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := discoverer.runs.Load(); got != 0 {
		t.Fatalf("discovery ran %d times for a signed-out agent, want 0", got)
	}
	// The caller still gets a usable catalog and is told why it is empty.
	if !strings.Contains(catalog.Warning, "signed out") {
		t.Fatalf("warning = %q, want it to explain the agent is signed out", catalog.Warning)
	}
	if !catalog.RefreshRecommended {
		t.Fatal("want RefreshRecommended so the picker retries after the user signs in")
	}
}

// Signing in must restore normal behavior with no further action.
func TestSignedInAgentRunsItsDiscoveryCommand(t *testing.T) {
	svc, discoverer := authGateService(ports.AgentAuthStatusAuthorized, nil)

	catalog, err := svc.Models(context.Background(), "kiro", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := discoverer.runs.Load(); got != 1 {
		t.Fatalf("discovery ran %d times for a signed-in agent, want 1", got)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %d, want the discovered catalog", len(catalog.Models))
	}
}

// Unknown is not a denial. A probe that cannot tell must not silently disable
// discovery — that would break model listing for every agent whose status AO
// cannot read, which before the Kiro probe fix was Kiro itself in both states.
func TestUnknownAuthStatusStillRunsDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status ports.AgentAuthStatus
		err    error
	}{
		{"probe cannot tell", ports.AgentAuthStatusUnknown, nil},
		{"probe failed outright", ports.AgentAuthStatusUnauthorized, errors.New("probe exploded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, discoverer := authGateService(tc.status, tc.err)
			if _, err := svc.Models(context.Background(), "kiro", "", false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := discoverer.runs.Load(); got != 1 {
				t.Fatalf("discovery ran %d times, want 1", got)
			}
		})
	}
}
