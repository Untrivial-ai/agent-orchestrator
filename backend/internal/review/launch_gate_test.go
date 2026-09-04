package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer half of Untrivial-ai/agent-orchestrator#4895. The reviewer child is
// created on its own path, not through Manager.Spawn, so a gate wired only into
// the worker seam would leave the reviewer able to strand at a startup prompt
// while the session it reviews looks healthy. That asymmetry is one of the
// split-brain shapes menard-software/setup-agent-orchestrator#418 recorded: a
// live reviewer tab beside an absent main child.

type reviewGateRecorder struct {
	decision ports.PreLaunchDecision
	err      error
	seen     []ports.PreLaunchRequest
}

func (g *reviewGateRecorder) PreLaunch(_ context.Context, req ports.PreLaunchRequest) (ports.PreLaunchDecision, error) {
	g.seen = append(g.seen, req)
	return g.decision, g.err
}

func TestLauncherSpawnRefusedByGateCreatesNoPane(t *testing.T) {
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{
		Allow: false, Reason: "workspace trust not accepted", PromptKind: "workspace_trust",
	}}
	rt := &fakeRuntime{}
	l := NewLauncher(fakeReviewerResolver{reviewer: &fakeReviewer{}, ok: true}, rt, t.TempDir(),
		WithLaunchGate(gate))

	_, err := l.Spawn(context.Background(), launchSpec())

	if !errors.Is(err, ports.ErrLaunchNotReady) {
		t.Fatalf("err = %v, want ports.ErrLaunchNotReady", err)
	}
	for _, want := range []string{"reviewer", "workspace trust not accepted", "workspace_trust"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to carry %q", err, want)
		}
	}
	if rt.created {
		t.Fatal("runtime.Create must not run when the gate refuses")
	}
	// P1 from review 5113322042: proving no Create is not enough. The reviewer
	// handle is destroyed to make way for the replacement, so a refusal that
	// destroys first leaves the session with no reviewer and no replacement --
	// strictly worse than not having asked.
	if rt.destroyed != "" {
		t.Fatalf("a refused launch destroyed the existing reviewer pane %q", rt.destroyed)
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate consulted %d times, want exactly once", len(gate.seen))
	}
}

func TestLauncherSpawnGateErrorAndZeroDecisionBothRefuse(t *testing.T) {
	for name, gate := range map[string]*reviewGateRecorder{
		"zero decision": {},
		"gate error":    {err: errors.New("gate unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &fakeRuntime{}
			l := NewLauncher(fakeReviewerResolver{reviewer: &fakeReviewer{}, ok: true}, rt, t.TempDir(),
				WithLaunchGate(gate))

			if _, err := l.Spawn(context.Background(), launchSpec()); !errors.Is(err, ports.ErrLaunchNotReady) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if rt.created {
				t.Fatal("runtime.Create must not run")
			}
			if rt.destroyed != "" {
				t.Fatalf("a refused launch destroyed the existing reviewer pane %q", rt.destroyed)
			}
		})
	}
}

// The parity requirement: the reviewer gate is shown the same resolved child
// environment as the worker gate, including a config root the reviewer inherited.
// The reported incident was exactly this -- the reviewer child ran with
// CLAUDE_CONFIG_DIR set to a root whose state file lacked every trusted path,
// while the operator's default root had them.
func TestLauncherSpawnGateSeesReviewerRoleAndEffectiveConfigRoot(t *testing.T) {
	const inherited = "/home/rose/.ao/bench-claude"
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{Allow: true}}
	rt := &fakeRuntime{}
	l := NewLauncher(
		fakeReviewerResolver{reviewer: &fakeReviewer{env: map[string]string{"CLAUDE_CONFIG_DIR": inherited}}, ok: true},
		rt, t.TempDir(), WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate consulted %d times, want exactly once", len(gate.seen))
	}
	req := gate.seen[0]
	if req.Role != ports.LaunchRoleReviewer {
		t.Fatalf("role = %q, want %q", req.Role, ports.LaunchRoleReviewer)
	}
	if got := req.Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("gate saw CLAUDE_CONFIG_DIR = %q, want the inherited %q", got, inherited)
	}
	if got := rt.createCfg.Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, want the same root the gate was shown", got)
	}
	if req.WorkspacePath == "" || len(req.Argv) == 0 {
		t.Fatal("request must carry the workspace and the exact child argv")
	}
}

func TestLauncherSpawnGateContributesEnvWithoutOverridingReviewerOwn(t *testing.T) {
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{
		Allow: true,
		Env:   map[string]string{"CLAUDE_CONFIG_DIR": "/gate/root", "GATE_ONLY": "kept"},
	}}
	rt := &fakeRuntime{}
	l := NewLauncher(
		fakeReviewerResolver{reviewer: &fakeReviewer{env: map[string]string{"CLAUDE_CONFIG_DIR": "/reviewer/root"}}, ok: true},
		rt, t.TempDir(), WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := rt.createCfg.Env["CLAUDE_CONFIG_DIR"]; got != "/reviewer/root" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the already-set value to win", got)
	}
	if got := rt.createCfg.Env["GATE_ONLY"]; got != "kept" {
		t.Fatalf("GATE_ONLY = %q, want the gate's own key to reach the child", got)
	}
}

func TestLauncherSpawnWithoutGateIsUnchanged(t *testing.T) {
	rt := &fakeRuntime{}
	l := NewLauncher(fakeReviewerResolver{reviewer: &fakeReviewer{}, ok: true}, rt, t.TempDir())

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn without a gate: %v", err)
	}
	if !rt.created {
		t.Fatal("runtime.Create must run when no gate is wired")
	}
}

// Reviewer half of #432. The measured incident was on a reviewer child: its
// environment carried CLAUDE_CONFIG_DIR pointing at a root whose state file had
// none of the trusted paths. Parity means the reviewer gate can take ownership
// of that root exactly as the worker gate can, and is bounded the same way.
func TestLauncherSpawnGateCanTakeOwnershipOfTheReviewerConfigRoot(t *testing.T) {
	const inherited = "/home/rose/.ao/bench-claude"
	const aoOwned = "/ao/data/claude-session-config/mer-1-reviewer"
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{
		Allow:       true,
		EnvOverride: map[string]string{"CLAUDE_CONFIG_DIR": aoOwned},
	}}
	rt := &fakeRuntime{}
	l := NewLauncher(
		fakeReviewerResolver{reviewer: &fakeReviewer{env: map[string]string{"CLAUDE_CONFIG_DIR": inherited}}, ok: true},
		rt, t.TempDir(), WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := gate.seen[0].Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("gate saw %q, want the inherited root", got)
	}
	if got := rt.createCfg.Env["CLAUDE_CONFIG_DIR"]; got != aoOwned {
		t.Fatalf("reviewer child CLAUDE_CONFIG_DIR = %q, want the AO-owned root %q", got, aoOwned)
	}
}

func TestLauncherSpawnGateOverrideCannotTakeAOOwnedVariables(t *testing.T) {
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{
		Allow: true,
		EnvOverride: map[string]string{
			"AO_REVIEW_SESSION_ID": "hijacked",
			"CLAUDE_CONFIG_DIR":    "/ao/owned",
		},
	}}
	rt := &fakeRuntime{}
	l := NewLauncher(fakeReviewerResolver{reviewer: &fakeReviewer{}, ok: true}, rt, t.TempDir(),
		WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := rt.createCfg.Env["AO_REVIEW_SESSION_ID"]; got == "hijacked" {
		t.Fatal("a gate override must not take an AO-owned reviewer variable")
	}
	if got := rt.createCfg.Env["CLAUDE_CONFIG_DIR"]; got != "/ao/owned" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the agent-owned variable overridable", got)
	}
}


// The permitted path still replaces the stale pane, so moving the gate earlier
// must not have cost the replacement it exists to perform.
func TestLauncherSpawnPermittedByGateStillReplacesTheStalePane(t *testing.T) {
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{Allow: true}}
	rt := &fakeRuntime{}
	l := NewLauncher(fakeReviewerResolver{reviewer: &fakeReviewer{}, ok: true}, rt, t.TempDir(),
		WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rt.destroyed == "" {
		t.Fatal("a permitted launch must still destroy the stale reviewer pane")
	}
	if !rt.created {
		t.Fatal("a permitted launch must create the replacement")
	}
}

// P1 from review 5113322042. The reviewer request omitted Kind, Harness,
// GitCommonDir and resolved Permissions, and substituted the reviewer command's
// working directory for the AO-created workspace. A gate asked to trust a path
// must be given the path AO owns.
func TestLauncherSpawnGateRequestCarriesFullParityFields(t *testing.T) {
	gate := &reviewGateRecorder{decision: ports.PreLaunchDecision{Allow: true}}
	rt := &fakeRuntime{}
	// The reviewer is told to run somewhere other than the AO worktree.
	reviewer := &fakeReviewer{workingDirectory: "/somewhere/else"}
	l := NewLauncher(fakeReviewerResolver{reviewer: reviewer, ok: true}, rt, t.TempDir(),
		WithLaunchGate(gate))

	if _, err := l.Spawn(context.Background(), launchSpec()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	req := gate.seen[0]
	if req.WorkspacePath != "/ws/mer-1" {
		t.Fatalf("WorkspacePath = %q, want the AO-created worktree, not the reviewer CWD", req.WorkspacePath)
	}
	if req.Harness == "" {
		t.Fatal("Harness must reach the gate")
	}
	if req.LaunchID != "run-1" {
		t.Fatalf("LaunchID = %q, want the reviewer run id", req.LaunchID)
	}
	if req.Role != ports.LaunchRoleReviewer {
		t.Fatalf("Role = %q", req.Role)
	}
}
