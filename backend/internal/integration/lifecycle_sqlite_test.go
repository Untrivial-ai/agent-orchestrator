package integration

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	prsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/pr"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type stubRuntime struct {
	created   int
	destroyed int
	// aliveByHandle scripts IsAlive per handle ID. If a handle ID is absent,
	// IsAlive returns true (default: alive), matching the pre-existing behavior
	// that all other tests relied on.
	aliveByHandle    map[string]bool
	destroyedHandles []string
}

func (s *stubRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	s.created++
	return ports.RuntimeHandle{ID: "h1"}, nil
}
func (s *stubRuntime) Destroy(_ context.Context, h ports.RuntimeHandle) error {
	s.destroyed++
	s.destroyedHandles = append(s.destroyedHandles, h.ID)
	return nil
}
func (s *stubRuntime) IsAlive(_ context.Context, h ports.RuntimeHandle) (bool, error) {
	if s.aliveByHandle != nil {
		if alive, ok := s.aliveByHandle[h.ID]; ok {
			return alive, nil
		}
	}
	return true, nil
}
func (s *stubRuntime) ProbeFencedRuntime(ctx context.Context, ref ports.FencedRuntimeRef) ports.FencedProbeResult {
	alive, err := s.IsAlive(ctx, ref.Handle)
	if err != nil {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
	}
	if alive {
		return ports.FencedProbeResult{Liveness: ports.FencedAlive, Reason: ports.FencedReasonExactMatch}
	}
	return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
}
func (s *stubRuntime) GetOutput(_ context.Context, _ ports.RuntimeHandle, _ int) (string, error) {
	return "", nil
}

// wasDestroyed reports whether Destroy was called with the given handle ID.
func (s *stubRuntime) wasDestroyed(handleID string) bool {
	for _, id := range s.destroyedHandles {
		if id == handleID {
			return true
		}
	}
	return false
}

type stubAgent struct{}

func (stubAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (stubAgent) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error) {
	return []string{"launch"}, nil
}
func (stubAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (stubAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }
func (stubAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]; id != "" {
		return []string{"resume", id}, true, nil
	}
	return nil, false, nil
}
func (stubAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

// stubAgents resolves every harness to the same stubAgent.
type stubAgents struct{}

func (stubAgents) Agent(domain.AgentHarness) (ports.Agent, bool) { return stubAgent{}, true }

type stubWorkspace struct{ destroyed int }

func (s *stubWorkspace) Create(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	return ports.WorkspaceInfo{Path: "/ws/" + string(cfg.SessionID), Branch: cfg.Branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}
func (s *stubWorkspace) Destroy(context.Context, ports.WorkspaceInfo) error {
	s.destroyed++
	return nil
}
func (s *stubWorkspace) Restore(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	return s.Create(ctx, cfg)
}
func (s *stubWorkspace) ForceDestroy(context.Context, ports.WorkspaceInfo) error { return nil }
func (s *stubWorkspace) StashUncommitted(_ context.Context, _ ports.WorkspaceInfo) (string, error) {
	return "", nil
}
func (s *stubWorkspace) ApplyPreserved(_ context.Context, _ ports.WorkspaceInfo, _ string) error {
	return nil
}

func (s *stubWorkspace) AddExclude(_ context.Context, _ ports.WorkspaceInfo, _ ...string) error {
	return nil
}

type captureMessenger struct{ msgs []string }

func (c *captureMessenger) Send(_ context.Context, _ domain.SessionID, msg string) error {
	c.msgs = append(c.msgs, msg)
	return nil
}

type stack struct {
	store *sqlite.Store
	sm    *sessionsvc.Service
	mgr   *sessionmanager.Manager
	lcm   *lifecycle.Manager
	prm   *prsvc.Manager
	rt    *stubRuntime
	ws    *stubWorkspace
	msg   *captureMessenger
}

func newStack(t *testing.T) *stack {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "mer",
		Path:         "/repo/mer",
		RegisteredAt: time.Now(),
		Config: domain.ProjectConfig{
			Worker:       domain.RoleOverride{Harness: domain.HarnessClaudeCode},
			Orchestrator: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
		},
	}); err != nil {
		t.Fatal(err)
	}
	msg := &captureMessenger{}
	lcm := lifecycle.New(store, msg)
	prm := prsvc.New(prsvc.Deps{Writer: store, Lifecycle: lcm})
	rt := &stubRuntime{}
	ws := &stubWorkspace{}
	mgr := sessionmanager.New(sessionmanager.Deps{Runtime: rt, Agents: stubAgents{}, Workspace: ws, Store: store, Messenger: msg, Lifecycle: lcm, LookPath: func(string) (string, error) { return "/usr/bin/true", nil }})
	lcm.SetCompletionTerminator(mgr)
	sm := sessionsvc.New(mgr, store)
	return &stack{store: store, sm: sm, mgr: mgr, lcm: lcm, prm: prm, rt: rt, ws: ws, msg: msg}
}

func TestMergedPRUsesSessionManagerOnlyWhenOptedIn(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.prm.ApplyObservation(ctx, sess.ID, ports.PRObservation{Fetched: true, URL: "pr1", Number: 1, Merged: true}); err != nil {
		t.Fatal(err)
	}
	if st.rt.destroyed != 0 || st.ws.destroyed != 0 {
		t.Fatalf("default policy tore down resources: runtime=%d workspace=%d", st.rt.destroyed, st.ws.destroyed)
	}
	rec, _, _ := st.store.GetSession(ctx, sess.ID)
	if rec.IsTerminated {
		t.Fatalf("default policy terminated merged session: %+v", rec)
	}

	sess2, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "c", Prompt: "do more"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.store.SetSessionTerminateOnPRMerge(ctx, sess2.ID, true, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("enable terminate-on-merge: ok=%v err=%v", ok, err)
	}
	if err := st.prm.ApplyObservation(ctx, sess2.ID, ports.PRObservation{Fetched: true, URL: "pr2", Number: 2, Merged: true}); err != nil {
		t.Fatal(err)
	}
	if st.rt.destroyed != 1 || st.ws.destroyed != 1 {
		t.Fatalf("opted-in merge teardown: runtime=%d workspace=%d, want 1/1", st.rt.destroyed, st.ws.destroyed)
	}
	rec, _, _ = st.store.GetSession(ctx, sess2.ID)
	if !rec.IsTerminated {
		t.Fatalf("opted-in merged session remained live: %+v", rec)
	}
}

func TestSpawnPRKillRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "mer-1" || sess.Status != domain.StatusIdle {
		t.Fatalf("spawn got %+v", sess)
	}
	rec, ok, _ := st.store.GetSession(ctx, sess.ID)
	if !ok || rec.Metadata.RuntimeHandleID != "h1" || rec.IsTerminated {
		t.Fatalf("post-spawn row wrong: %+v", rec)
	}
	if err := st.prm.ApplyObservation(ctx, sess.ID, ports.PRObservation{Fetched: true, URL: "pr1", Number: 1, CI: domain.CIFailing, Checks: []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.sm.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusCIFailed {
		t.Fatalf("want ci_failed, got %q", got.Status)
	}
	freed, err := st.sm.Kill(ctx, sess.ID)
	if err != nil || !freed {
		t.Fatalf("kill freed=%v err=%v", freed, err)
	}
	rec, _, _ = st.store.GetSession(ctx, sess.ID)
	if !rec.IsTerminated {
		t.Fatalf("post-kill row should be terminated: %+v", rec)
	}
}

func TestRestoreRoundTripPreservesMetadata(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _, _ := st.store.GetSession(ctx, sess.ID)
	rec.Metadata.AgentSessionID = "agent-x"
	if err := st.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := st.sm.Kill(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := st.sm.Restore(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.IsTerminated || restored.Session.Metadata.AgentSessionID != "agent-x" {
		t.Fatalf("restored wrong: %+v", restored)
	}
}

// TestReconcile_PreservesFailedLiveSessionAndReapsLeakedTmux exercises
// Manager.Reconcile against a real sqlite.Store:
//
//   - Session A: is_terminated=0 but its runtime is GONE and it is a promptless
//     KindWorker. Its blank relaunch is not resumable, so reconcileLive preserves
//     the live record and worktree with exited activity. End state:
//     is_terminated=false, runtime.Create count stays 0.
//   - Session B: is_terminated=1 but its runtime is still ALIVE (leaked teardown)
//     => Reconcile must call Destroy on its handle.
func TestReconcile_PreservesFailedLiveSessionAndReapsLeakedTmux(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)

	// Script liveness: handle "hdl-A" is dead; handle "hdl-B" is alive.
	st.rt.aliveByHandle = map[string]bool{
		"hdl-A": false,
		"hdl-B": true,
	}

	now := time.Now().UTC()

	// Seed session A: live in the DB (is_terminated=0) but runtime is gone.
	// WorkspacePath and Branch must be non-empty so reconcileLive actually probes
	// IsAlive (it short-circuits on missing path/branch).
	recA := domain.SessionRecord{
		ProjectID:    "mer",
		Kind:         domain.KindWorker,
		Harness:      domain.HarnessClaudeCode,
		IsTerminated: false,
		Metadata: domain.SessionMetadata{
			Branch:          "ao/mer-a/root",
			WorkspacePath:   "/ws/mer-a",
			RuntimeHandleID: "hdl-A",
		},
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	}
	recA, err := st.store.CreateSession(ctx, recA)
	if err != nil {
		t.Fatalf("seed session A: %v", err)
	}

	// Seed session B: terminated in the DB (is_terminated=1) but runtime leaked.
	recB := domain.SessionRecord{
		ProjectID:    "mer",
		Kind:         domain.KindWorker,
		Harness:      domain.HarnessClaudeCode,
		IsTerminated: true,
		Metadata: domain.SessionMetadata{
			Branch:          "ao/mer-b/root",
			WorkspacePath:   "/ws/mer-b",
			RuntimeHandleID: "hdl-B",
		},
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	}
	recB, err = st.store.CreateSession(ctx, recB)
	if err != nil {
		t.Fatalf("seed session B: %v", err)
	}
	// recB is already built with IsTerminated=true, so CreateSession stores it terminated; the UpdateSession below is redundant but kept for clarity.
	if err := st.store.UpdateSession(ctx, recB); err != nil {
		t.Fatalf("patch session B terminated: %v", err)
	}

	if err := st.mgr.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Session A is a promptless KindWorker: reconcileLive cannot safely launch it
	// blank, but must not treat that restart-time failure as a durable termination.
	// It remains available for explicit recovery with its worktree intact.
	gotA, ok, err := st.store.GetSession(ctx, recA.ID)
	if err != nil {
		t.Fatalf("get session A: %v", err)
	}
	if !ok {
		t.Fatalf("session A: not found after Reconcile")
	}
	if gotA.IsTerminated {
		t.Fatalf("session A: restart-time relaunch failure terminated a recoverable session: %+v", gotA)
	}
	if gotA.Activity.State != domain.ActivityExited {
		t.Fatalf("session A: activity = %q, want %q", gotA.Activity.State, domain.ActivityExited)
	}
	if gotA.Metadata.WorkspacePath != recA.Metadata.WorkspacePath || gotA.Metadata.Branch != recA.Metadata.Branch {
		t.Fatalf("session A: workspace identity changed: got %+v, want %+v", gotA.Metadata, recA.Metadata)
	}
	// No runtime.Create: a promptless worker must not be blank-relaunched.
	if st.rt.created != 0 {
		t.Fatalf("want 0 runtime Creates (promptless worker must not relaunch), got %d", st.rt.created)
	}

	// Session B's leaked runtime must have been destroyed.
	if !st.rt.wasDestroyed("hdl-B") {
		t.Fatalf("session B: want Destroy called for handle hdl-B; destroyed handles: %v", st.rt.destroyedHandles)
	}
}

func TestCDCPollerReceivesSessionAndPREvents(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	b := cdc.NewBroadcaster()
	var got []cdc.Event
	b.Subscribe(func(e cdc.Event) { got = append(got, e) })
	poller := cdc.NewPoller(st.store, b, cdc.PollerConfig{})
	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.prm.ApplyObservation(ctx, sess.ID, ports.PRObservation{Fetched: true, URL: "pr1", Number: 1, Review: domain.ReviewApproved}); err != nil {
		t.Fatal(err)
	}
	if err := poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("want CDC events, got %d", len(got))
	}
}
