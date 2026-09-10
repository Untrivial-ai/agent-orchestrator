package sessionmanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These cover Untrivial-ai/agent-orchestrator#4895, reported downstream as
// menard-software/setup-agent-orchestrator#418: AO created Claude children that
// stopped before their agent loop, and reported them as ordinary work. Process
// creation is not successful startup, and the only place that can be fixed is
// before the child exists.
//
// They are written at the same seam as TestSpawn_RejectsMissingAgentBinary,
// because that check already proves the shape a pre-spawn refusal must have:
// runtime.Create must not run, and the workspace must be torn down.

type recordingLaunchGate struct {
	decision ports.PreLaunchDecision
	err      error
	seen     []ports.PreLaunchRequest
}

func (g *recordingLaunchGate) PreLaunch(_ context.Context, req ports.PreLaunchRequest) (ports.PreLaunchDecision, error) {
	g.seen = append(g.seen, req)
	return g.decision, g.err
}

func gateSpawnDeps(t *testing.T, gate ports.LaunchGate) (*fakeRuntime, *fakeWorkspace, Deps) {
	t.Helper()
	return gateSpawnDepsWithProjectEnv(t, gate, nil)
}

func gateSpawnDepsWithProjectEnv(t *testing.T, gate ports.LaunchGate, projectEnv map[string]string) (*fakeRuntime, *fakeWorkspace, Deps) {
	t.Helper()
	st := newFakeStore()
	config := testRoleAgents()
	config.Env = projectEnv
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: config}
	rt := &fakeRuntime{}
	ws := &fakeWorkspace{}
	return rt, ws, Deps{
		Runtime: rt, Agents: fakeAgents{}, Workspace: ws, Store: st,
		Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st},
		DataDir: t.TempDir(), LaunchGate: gate,
		// The binary check runs before the gate; stub it so these tests exercise
		// the gate rather than PATH resolution.
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

// A gate that refuses must stop the spawn before any child exists, exactly like
// the missing-binary check. Without this, AO produces a session card for a
// child that was never able to become ready -- the #418 symptom.
func TestSpawn_LaunchGateRefusalStopsBeforeChildCreation(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow: false, Reason: "workspace trust not accepted", PromptKind: "workspace_trust",
	}}
	rt, ws, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})

	if !errors.Is(err, ports.ErrLaunchNotReady) {
		t.Fatalf("err = %v, want ports.ErrLaunchNotReady", err)
	}
	if !errors.Is(err, ErrSpawnLaunchGate) {
		t.Fatalf("err = %v, want it to name the launch-readiness spawn stage", err)
	}
	for _, want := range []string{"workspace trust not accepted", "workspace_trust"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to carry %q so the cause is actionable", err, want)
		}
	}
	if rt.created != 0 {
		t.Fatal("runtime.Create must NOT run when the launch gate refuses")
	}
	if ws.destroyed != 1 {
		t.Fatal("workspace must be torn down when the pre-launch gate refuses")
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate consulted %d times, want exactly once", len(gate.seen))
	}
}

// The zero decision refuses. A gate that returns nothing by mistake must stop
// the launch rather than wave it through.
func TestSpawn_LaunchGateZeroDecisionRefuses(t *testing.T) {
	rt, _, deps := gateSpawnDeps(t, &recordingLaunchGate{})
	m := New(deps)

	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})

	if !errors.Is(err, ports.ErrLaunchNotReady) {
		t.Fatalf("err = %v, want the zero decision to refuse", err)
	}
	if rt.created != 0 {
		t.Fatal("runtime.Create must NOT run for a zero decision")
	}
}

// A gate error is a refusal, not a warning: an unreachable or broken gate must
// not silently degrade into an ungated launch.
func TestSpawn_LaunchGateErrorRefusesRatherThanDegrades(t *testing.T) {
	gate := &recordingLaunchGate{err: errors.New("gate unavailable")}
	rt, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})

	if !errors.Is(err, ports.ErrLaunchNotReady) {
		t.Fatalf("err = %v, want a gate error to fail closed", err)
	}
	if !strings.Contains(err.Error(), "gate unavailable") {
		t.Fatalf("err = %v, want the underlying gate error preserved", err)
	}
	if rt.created != 0 {
		t.Fatal("runtime.Create must NOT run when the gate itself fails")
	}
}

// A permitted launch reaches the child, and the gate's environment contribution
// reaches the exact child AO is about to create. This is the half of #4895 a
// post-spawn helper cannot do.
func TestSpawn_LaunchGateContributesChildEnvironment(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow: true,
		Env:   map[string]string{"CLAUDE_CONFIG_DIR": "/isolated/session/config"},
	}}
	rt, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rt.created != 1 {
		t.Fatalf("runtime.Create ran %d times, want 1", rt.created)
	}
	if got := rt.lastCfg.Env["CLAUDE_CONFIG_DIR"]; got != "/isolated/session/config" {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, want the gate's value", got)
	}
}

// The gate contributes; it does not take over. An AO-owned variable must win,
// so a gate cannot redirect the session's own data directory or run file.
func TestSpawn_LaunchGateCannotOverrideAOOwnedEnvironment(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow: true,
		Env:   map[string]string{"AO_SESSION_ID": "someone-else", "GATE_ONLY": "kept"},
	}}
	rt, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := rt.lastCfg.Env["AO_SESSION_ID"]; got == "someone-else" {
		t.Fatal("a gate must not overwrite an AO-owned environment variable")
	}
	if got := rt.lastCfg.Env["GATE_ONLY"]; got != "kept" {
		t.Fatalf("GATE_ONLY = %q, want the gate's own key to survive", got)
	}
}

// The request carries daemon-owned values only, and the argv the child will
// actually run, so a gate can confirm a resolved permission mode reaches it.
func TestSpawn_LaunchGateSeesResolvedDaemonOwnedValues(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	_, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate consulted %d times, want exactly once", len(gate.seen))
	}
	req := gate.seen[0]
	if req.SessionID == "" {
		t.Fatal("request must carry the AO session id")
	}
	if req.WorkspacePath == "" {
		t.Fatal("request must carry the AO-created workspace path")
	}
	if req.Kind != domain.KindWorker {
		t.Fatalf("request kind = %v, want the spawn's kind", req.Kind)
	}
	if len(req.Argv) == 0 {
		t.Fatal("request must carry the exact child argv")
	}
}

// Nil gate is the default and must leave spawn byte-identical.
func TestSpawn_WithoutLaunchGateIsUnchanged(t *testing.T) {
	rt, _, deps := gateSpawnDeps(t, nil)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn without a gate: %v", err)
	}
	if rt.created != 1 {
		t.Fatalf("runtime.Create ran %d times, want 1", rt.created)
	}
}



// The reported #4895 condition, at the worker seam.
//
// Trust was recorded true in the operator's home Claude root for the exact
// worktree paths, and was absent from the root the child actually read, because
// CLAUDE_CONFIG_DIR was already set in the inherited environment. Every surface
// said the state existed; the child still stopped at the prompt that state was
// meant to answer.
//
// A gate cannot detect that unless it is told the resolved child environment.
// Guessing the default root is precisely the mistake that produced the incident.
func TestSpawn_LaunchGateSeesTheEffectiveConfigRootFromTheChildEnvironment(t *testing.T) {
	const inherited = "/home/rose/.ao/bench-claude"
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	// Model the operator's environment: a config root is already chosen for the
	// child before AO ever consults a gate.
	rt, _, deps := gateSpawnDepsWithProjectEnv(t, gate, map[string]string{"CLAUDE_CONFIG_DIR": inherited})
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate consulted %d times, want exactly once", len(gate.seen))
	}
	observed := gate.seen[0].Env["CLAUDE_CONFIG_DIR"]
	if observed != inherited {
		t.Fatalf("gate saw CLAUDE_CONFIG_DIR = %q, want the inherited %q; without it a gate "+
			"writes its state to a root the child never reads", observed, inherited)
	}
	if got := rt.lastCfg.Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, want the same root the gate was shown", got)
	}
}

// The gate is told which child it is being asked about, so worker and reviewer
// cannot be silently conflated.
func TestSpawn_LaunchGateRequestNamesTheWorkerRole(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	_, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := gate.seen[0].Role; got != ports.LaunchRoleWorker {
		t.Fatalf("role = %q, want %q", got, ports.LaunchRoleWorker)
	}
}

// The gate gets a copy. Reaching into the child environment directly would let
// a gate change a launch it had already been asked to judge.
func TestSpawn_LaunchGateCannotMutateTheChildEnvironmentThroughItsRequest(t *testing.T) {
	gate := &mutatingLaunchGate{}
	rt, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, leaked := rt.lastCfg.Env["SNEAKED_IN"]; leaked {
		t.Fatal("a gate must not reach the child environment except through its decision")
	}
}

type mutatingLaunchGate struct{}

func (mutatingLaunchGate) PreLaunch(_ context.Context, req ports.PreLaunchRequest) (ports.PreLaunchDecision, error) {
	req.Env["SNEAKED_IN"] = "1"
	return ports.PreLaunchDecision{Allow: true}, nil
}

// menard-software/setup-agent-orchestrator#432, the follow-on to the gate
// itself. Seeing the effective config root is not enough: if the child inherits
// CLAUDE_CONFIG_DIR from the operator's environment, a contribute-only gate
// cannot put the child and the state it seeds in the same root. It writes trust
// to the root it owns, the child reads the root it inherited, and the child
// stops at a prompt whose answer exists in a file it never opens.
//
// That is the measured incident: trust true in the default root for the exact
// worktree paths, absent from the effective inherited root.
func TestSpawn_LaunchGateCanTakeOwnershipOfTheAgentConfigRoot(t *testing.T) {
	const inherited = "/home/rose/.ao/bench-claude"
	const aoOwned = "/ao/data/claude-session-config/mer-1"
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow:       true,
		EnvOverride: map[string]string{"CLAUDE_CONFIG_DIR": aoOwned},
	}}
	rt, _, deps := gateSpawnDepsWithProjectEnv(t, gate, map[string]string{"CLAUDE_CONFIG_DIR": inherited})
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := gate.seen[0].Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("gate saw %q, want the inherited root it must decide about", got)
	}
	if got := rt.lastCfg.Env["CLAUDE_CONFIG_DIR"]; got != aoOwned {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, want the AO-owned root %q; a gate that "+
			"cannot redirect it seeds trust into a file the child never reads", got, aoOwned)
	}
}

// An override is bounded. A gate may take a variable the agent owns; it may not
// take one the daemon owns, or it could redirect a session's own reporting.
func TestSpawn_LaunchGateOverrideCannotTakeAOOwnedVariables(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow: true,
		EnvOverride: map[string]string{
			"AO_SESSION_ID":     "someone-else",
			"AO_DATA_DIR":       "/tmp/elsewhere",
			"CLAUDE_CONFIG_DIR": "/ao/owned",
		},
	}}
	rt, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := rt.lastCfg.Env["AO_SESSION_ID"]; got == "someone-else" {
		t.Fatal("a gate override must not take an AO-owned variable")
	}
	if got := rt.lastCfg.Env["AO_DATA_DIR"]; got == "/tmp/elsewhere" {
		t.Fatal("a gate override must not redirect the session data dir")
	}
	if got := rt.lastCfg.Env["CLAUDE_CONFIG_DIR"]; got != "/ao/owned" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the agent-owned variable to be overridable", got)
	}
}

// P1 from review 5113322042: a gate cannot bind a decision to the current
// (session, launch, conversation) unless the identity exists before it is
// asked. Spawn used to generate the launch id after the gate.
func TestSpawn_LaunchGateSeesTheLaunchIdentityItWillRunUnder(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	_, _, deps := gateSpawnDeps(t, gate)
	deps.NewLaunchID = func() string { return "launch-under-test" }
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := gate.seen[0].LaunchID; got != "launch-under-test" {
		t.Fatalf("gate saw LaunchID %q, want the id the child runs under", got)
	}
}

// The gate's request must carry every daemon-owned value the port promises, or
// a gate that needs one cannot make the same decision on both paths.
func TestSpawn_LaunchGateRequestCarriesTheFullParityFields(t *testing.T) {
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	_, _, deps := gateSpawnDeps(t, gate)
	m := New(deps)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	req := gate.seen[0]
	for name, empty := range map[string]bool{
		"SessionID":     req.SessionID == "",
		"WorkspacePath": req.WorkspacePath == "",
		"LaunchID":      req.LaunchID == "",
		"Argv":          len(req.Argv) == 0,
		"Role":          req.Role == "",
	} {
		if empty {
			t.Fatalf("request field %s is empty", name)
		}
	}
	if req.Kind != domain.KindWorker {
		t.Fatalf("Kind = %q, want the spawn's kind", req.Kind)
	}
}

// P1 from review 5113322042, the one the reviewer called out as "not merely
// future scope": Restore rebuilds env and argv and creates a child without ever
// consulting the gate, so a restored Claude child kept its inherited
// CLAUDE_CONFIG_DIR while the older trust writer seeded the default root. The
// exact two-roots condition, reachable through a path this PR had not gated.
func TestRestore_LaunchGateRunsBeforeTheRelaunchedChild(t *testing.T) {
	const inherited = "/home/rose/.ao/bench-claude"
	st := newFakeStore()
	config := testRoleAgents()
	config.Env = map[string]string{"CLAUDE_CONFIG_DIR": inherited}
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: config}
	rt := &fakeRuntime{}
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{Allow: true}}
	m := New(Deps{
		Runtime: rt, Agents: fakeAgents{}, Workspace: &fakeWorkspace{}, Store: st,
		Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st}, LaunchGate: gate,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "b", AgentSessionID: "agent-x"})

	if _, err := m.RestoreWithMode(ctx, "mer-1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(gate.seen) == 0 {
		t.Fatal("restore relaunched a child without consulting the launch gate")
	}
	req := gate.seen[0]
	if got := req.Env["CLAUDE_CONFIG_DIR"]; got != inherited {
		t.Fatalf("gate saw CLAUDE_CONFIG_DIR = %q, want the inherited %q", got, inherited)
	}
	if req.LaunchID == "" {
		t.Fatal("a restored launch must carry its identity to the gate")
	}
	if req.Role != ports.LaunchRoleWorker {
		t.Fatalf("Role = %q, want worker", req.Role)
	}
}

// A refusal on restore must not leave a replaced child: nothing is created.
func TestRestore_LaunchGateRefusalCreatesNoChild(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	rt := &fakeRuntime{}
	gate := &recordingLaunchGate{decision: ports.PreLaunchDecision{
		Allow: false, Reason: "workspace trust not accepted", PromptKind: "workspace_trust",
	}}
	m := New(Deps{
		Runtime: rt, Agents: fakeAgents{}, Workspace: &fakeWorkspace{}, Store: st,
		Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st}, LaunchGate: gate,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "b", AgentSessionID: "agent-x"})

	_, err := m.RestoreWithMode(ctx, "mer-1")

	if err == nil {
		t.Fatal("a refused restore must fail rather than relaunch")
	}
	if !errors.Is(err, ports.ErrLaunchNotReady) {
		t.Fatalf("err = %v, want ports.ErrLaunchNotReady", err)
	}
	if rt.created != 0 {
		t.Fatalf("runtime.Create ran %d times on a refused restore", rt.created)
	}
}
