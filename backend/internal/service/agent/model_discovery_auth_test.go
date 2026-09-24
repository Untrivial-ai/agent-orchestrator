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
type countingDiscoverer struct {
	runs           atomic.Int32
	canPromptLogin bool
}

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

// canPromptLogin mirrors whether this agent's discovery can start a sign-in.
// The gate only applies when it can.
func (d *countingDiscoverer) DiscoveryCanPromptLogin(string) bool { return d.canPromptLogin }

func authGateService(status ports.AgentAuthStatus, authErr error) (*Service, *countingDiscoverer) {
	discoverer := &countingDiscoverer{canPromptLogin: true}
	stub := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "kiro-cli", nil },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return status, authErr },
	}
	agents := []agentregistry.HarnessAgent{readinessHarness("kiro", "Kiro", stub)}
	return newService(agents, nil, nil, discoverer), discoverer
}

// envAwareAgent models an adapter that can take its credential from the
// project-scoped environment, as Kiro does with KIRO_API_KEY.
type envAwareAgent struct {
	*readinessTestAgent
	sawEnv map[string]string
}

func (a *envAwareAgent) AuthStatusInEnv(ctx context.Context, env map[string]string) (ports.AgentAuthStatus, error) {
	a.sawEnv = env
	if strings.TrimSpace(env["KIRO_API_KEY"]) != "" {
		return ports.AgentAuthStatusAuthorized, nil
	}
	return a.AuthStatus(ctx)
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

// The gate must answer for the environment the discovery command would run in,
// not the daemon's own: an adapter can take its credential from that overlay,
// as Kiro does with KIRO_API_KEY, and asking about the wrong one reports a
// signed-in agent as signed out.
//
// #5051 made catalogs global, so the service no longer builds request.Env from
// project config and no production caller currently supplies one. These drive
// discoverModels directly rather than through Models(), so they pin the
// contract for whoever reintroduces a scoped environment.
// https://github.com/Untrivial-ai/agent-orchestrator/pull/5321#discussion_r3999115728
func TestGateAsksAboutTheEnvironmentDiscoveryWouldUse(t *testing.T) {
	discoverer := &countingDiscoverer{canPromptLogin: true}
	stub := &envAwareAgent{readinessTestAgent: &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "kiro-cli", nil },
		// The daemon's own environment has no key, so this is what a
		// non-env-aware question would have answered.
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusUnauthorized, nil
		},
	}}
	item := readinessHarness("kiro", "Kiro", stub)
	svc := newService([]agentregistry.HarnessAgent{item}, nil, nil, discoverer)

	request := ports.AgentModelDiscoveryRequest{
		AgentID: "kiro",
		Binary:  "kiro-cli",
		Env:     map[string]string{"KIRO_API_KEY": "scoped-key"},
	}
	if _, err := svc.discoverModels(context.Background(), item, "kiro", request); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := discoverer.runs.Load(); got != 1 {
		t.Fatalf("discovery ran %d times with a scoped credential, want 1", got)
	}
	if stub.sawEnv["KIRO_API_KEY"] != "scoped-key" {
		t.Fatalf("auth check saw env %#v, want the overlay discovery runs under", stub.sawEnv)
	}
}

// An overlay must not become a blanket excuse to skip the gate.
func TestScopedEnvWithoutCredentialStillBlocks(t *testing.T) {
	discoverer := &countingDiscoverer{canPromptLogin: true}
	stub := &envAwareAgent{readinessTestAgent: &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "kiro-cli", nil },
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusUnauthorized, nil
		},
	}}
	item := readinessHarness("kiro", "Kiro", stub)
	svc := newService([]agentregistry.HarnessAgent{item}, nil, nil, discoverer)

	request := ports.AgentModelDiscoveryRequest{
		AgentID: "kiro", Binary: "kiro-cli",
		Env: map[string]string{"UNRELATED": "1"},
	}
	if _, err := svc.discoverModels(context.Background(), item, "kiro", request); err == nil {
		t.Fatal("want the gate to block a signed-out agent")
	}
	if got := discoverer.runs.Load(); got != 0 {
		t.Fatalf("discovery ran %d times for a signed-out agent, want 0", got)
	}
}

// An adapter that can only answer for the daemon's environment must not block a
// run whose environment it never saw.
func TestEnvUnawareAdapterDoesNotBlockUnderAnOverlay(t *testing.T) {
	discoverer := &countingDiscoverer{canPromptLogin: true}
	stub := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "kiro-cli", nil },
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusUnauthorized, nil
		},
	}
	item := readinessHarness("kiro", "Kiro", stub)
	svc := newService([]agentregistry.HarnessAgent{item}, nil, nil, discoverer)

	request := ports.AgentModelDiscoveryRequest{
		AgentID: "kiro", Binary: "kiro-cli",
		Env: map[string]string{"SOMETHING": "1"},
	}
	if _, err := svc.discoverModels(context.Background(), item, "kiro", request); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := discoverer.runs.Load(); got != 1 {
		t.Fatalf("discovery ran %d times, want 1 — a stale-environment answer must not block", got)
	}
}

// Claude Code's catalog is static — Discover returns a fixed list and never
// spawns anything, so it cannot start a sign-in. Gating it on auth would strip
// the model picker from every signed-out Claude Code user to prevent a risk
// that does not exist for them.
func TestStaticCatalogIsNotGatedOnAuth(t *testing.T) {
	discoverer := &countingDiscoverer{canPromptLogin: false}
	stub := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "claude", nil },
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusUnauthorized, nil
		},
	}
	agents := []agentregistry.HarnessAgent{readinessHarness("claude-code", "Claude Code", stub)}
	svc := newService(agents, nil, nil, discoverer)

	catalog, err := svc.Models(context.Background(), "claude-code", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := discoverer.runs.Load(); got != 1 {
		t.Fatalf("discovery ran %d times for a static catalog, want 1 even when signed out", got)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %d, want the static catalog a signed-out user still gets", len(catalog.Models))
	}
	if catalog.Warning != "" {
		t.Fatalf("warning = %q, want none: nothing was withheld", catalog.Warning)
	}
}
