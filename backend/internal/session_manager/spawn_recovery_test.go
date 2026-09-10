package sessionmanager

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type cancellationAwareStartupStore struct{ *fakeStore }

func (s cancellationAwareStartupStore) DeleteSession(ctx context.Context, id domain.SessionID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.fakeStore.DeleteSession(ctx, id)
}

type cancellationAwareStartupLifecycle struct{ *fakeLCM }

func (l cancellationAwareStartupLifecycle) MarkTerminated(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.fakeLCM.MarkTerminated(ctx, id)
}

type timeoutStartupWorkspace struct{ *fakeWorkspace }

func (w timeoutStartupWorkspace) Create(ctx context.Context, _ ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	<-ctx.Done()
	return ports.WorkspaceInfo{}, ctx.Err()
}

func TestSpawnTimeoutDoesNotLeaveActiveSeed(t *testing.T) {
	m, st, _, ws := newManager()
	m.store = cancellationAwareStartupStore{st}
	m.lcm = cancellationAwareStartupLifecycle{&fakeLCM{store: st}}
	m.workspace = timeoutStartupWorkspace{ws}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spawn error = %v, expected deadline exceeded", err)
	}
	if rec, exists := st.sessions["mer-1"]; exists && !rec.IsTerminated {
		t.Fatalf("timed-out spawn left active seed: runtime=%q workspace=%q", rec.Metadata.RuntimeHandleID, rec.Metadata.WorkspacePath)
	}
}

type startupEffectFailure struct {
	cause   error
	handle  ports.RuntimeHandle
	cleanup ports.RuntimeCleanupOutcome
}

func (e startupEffectFailure) Error() string                       { return e.cause.Error() }
func (e startupEffectFailure) Unwrap() error                       { return e.cause }
func (e startupEffectFailure) PossibleHandle() ports.RuntimeHandle { return e.handle }
func (e startupEffectFailure) EffectOutcome() ports.RuntimeEffectOutcome {
	return ports.RuntimeEffectPossible
}
func (e startupEffectFailure) CleanupOutcome() ports.RuntimeCleanupOutcome { return e.cleanup }

type cancelledRuntimeCreate struct {
	*fakeRuntime
	cancel         context.CancelFunc
	err            error
	cleanupContext context.Context
}

func (r *cancelledRuntimeCreate) Create(context.Context, ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.cancel()
	return ports.RuntimeHandle{}, r.err
}
func (r *cancelledRuntimeCreate) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	r.cleanupContext = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.fakeRuntime.Destroy(ctx, handle)
}

func TestSpawnRuntimeEffectCleanupSurvivesRequestCancellation(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "uncertain"}[cleanupFails], func(t *testing.T) {
			m, st, rt, ws := newManager()
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopErr := errors.New("runtime inventory unavailable")
			if cleanupFails {
				rt.destroyErr = stopErr
			}
			runtime := &cancelledRuntimeCreate{fakeRuntime: rt, cancel: cancel, err: startupEffectFailure{cause: context.Canceled, handle: ports.RuntimeHandle{ID: "exact-created-handle"}}}
			m.runtime = runtime
			_, _, _, err := m.Spawn(callCtx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("spawn error = %v", err)
			}
			if len(rt.destroyedIDs) != 1 || rt.destroyedIDs[0] != "exact-created-handle" {
				t.Fatalf("destroyed handles = %v", rt.destroyedIDs)
			}
			deadline, ok := runtime.cleanupContext.Deadline()
			if !ok || time.Until(deadline) > spawnCleanupBudget {
				t.Fatalf("cleanup lacks bounded deadline: %v", deadline)
			}
			if cleanupFails {
				if !errors.Is(err, stopErr) {
					t.Fatalf("cleanup error lost: %v", err)
				}
				rec := st.sessions["mer-1"]
				if rec.IsTerminated || rec.Metadata.Startup == nil || !rec.Metadata.Startup.RuntimePossible || rec.Metadata.RuntimeHandleID != "exact-created-handle" || ws.destroyed != 0 {
					t.Fatalf("uncertain cleanup lost ownership: %+v; destroyed workspaces=%d", rec, ws.destroyed)
				}
				rt.destroyErr = nil
				if err := m.reconcileStartups(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, exists := st.sessions["mer-1"]; exists {
					t.Fatal("recovered seed remains")
				}
			} else if _, exists := st.sessions["mer-1"]; exists {
				t.Fatal("cleaned seed remains")
			}
		})
	}
}

func TestStartupRecoveryPreservesUncertainRuntimeAndHealthySessions(t *testing.T) {
	m, st, rt, ws := newManager()
	uncertain := mkLive("mer-1")
	uncertain.Metadata.RuntimeHandleID = ""
	uncertain.Metadata.RuntimeLaunchID = "launch-one"
	uncertain.Metadata.Startup = &domain.SessionStartup{ID: "startup-one", Stage: "runtime_creating", RuntimePossible: true}
	st.sessions[uncertain.ID] = uncertain
	chat := mkLive("mer-2")
	chat.Mode = domain.SessionModeChat
	chat.Metadata.RuntimeHandleID = ""
	st.sessions[chat.ID] = chat
	committed := mkLive("mer-3")
	st.sessions[committed.ID] = committed
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("recovery destroyed an unproven resource")
	}
	if rec := st.sessions[uncertain.ID]; rec.IsTerminated || rec.Metadata.Startup == nil || rec.Metadata.Startup.Stage != "cleanup_pending" || rec.Metadata.Startup.LastError == "" {
		t.Fatalf("uncertain startup facts = %+v", rec)
	}
	if st.sessions[chat.ID].IsTerminated || st.sessions[committed.ID].IsTerminated {
		t.Fatal("healthy session was terminated")
	}
}

func TestStartupRecoverySkipsInFlightOperationAndNewerLaunch(t *testing.T) {
	m, st, rt, ws := newManager()
	rec, attempt, err := m.createSpawnSeed(context.Background(), ports.SpawnConfig{ProjectID: "mer"}, testRoleAgents())
	if err != nil {
		t.Fatal(err)
	}
	defer m.endAgentOperation(rec.ID, agentOperationSpawn)
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists := st.sessions[rec.ID]; !exists {
		t.Fatal("in-flight seed was reclaimed")
	}
	replacement := st.sessions[rec.ID]
	replacement.Metadata.Startup = nil
	replacement.Metadata.RuntimeLaunchID = "replacement"
	st.sessions[rec.ID] = replacement
	if err := m.rollbackStartup(context.Background(), attempt, errors.New("old startup")); err == nil {
		t.Fatal("stale cleanup unexpectedly succeeded")
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("stale operation destroyed replacement resources")
	}
}

type startupCompletionReadFailure struct {
	*fakeStore
	completed bool
}

func (s *startupCompletionReadFailure) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	if err := s.fakeStore.UpdateSession(ctx, rec); err != nil {
		return err
	}
	if rec.Metadata.Startup == nil && rec.Metadata.RuntimeHandleID != "" {
		s.completed = true
	}
	return nil
}
func (s *startupCompletionReadFailure) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	if s.completed {
		return domain.SessionRecord{}, false, context.Canceled
	}
	return s.fakeStore.GetSession(ctx, id)
}
func TestSpawnLostResponsePreservesCommittedLaunch(t *testing.T) {
	m, st, rt, ws := newManager()
	m.store = &startupCompletionReadFailure{fakeStore: st}
	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("response error = %v", err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("response read error undid committed spawn")
	}
	if rec := st.sessions["mer-1"]; rec.IsTerminated || rec.Metadata.Startup != nil || rec.Metadata.RuntimeHandleID == "" {
		t.Fatalf("committed state = %+v", rec)
	}
}

type durableStartupLifecycle struct {
	*fakeLCM
	store *sqlite.Store
}

func (l durableStartupLifecycle) MarkSpawned(ctx context.Context, id domain.SessionID, metadata domain.SessionMetadata) error {
	rec, ok, err := l.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	rec.Metadata = metadata
	return l.store.CommitSessionSpawn(ctx, rec)
}
func (l durableStartupLifecycle) MarkTerminated(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := l.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	rec.IsTerminated = true
	return l.store.UpdateSession(ctx, rec)
}

func newDurableStartupManager(t *testing.T) (*Manager, *sqlite.Store, *fakeRuntime, *fakeWorkspace) {
	t.Helper()
	s := sqlitetest.MustOpen(t)
	if err := s.UpsertProject(context.Background(), domain.ProjectRecord{ID: "mer", Path: t.TempDir(), Config: testRoleAgents(), RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m, st, rt, ws := newManager()
	m.store = s
	m.lcm = durableStartupLifecycle{fakeLCM: &fakeLCM{store: st}, store: s}
	m.dataDir = t.TempDir()
	return m, s, rt, ws
}

func TestSpawnCancelledHTTPRequestRollsBackSQLiteSeed(t *testing.T) {
	m, s, _, ws := newDurableStartupManager(t)
	callCtx, cancel := context.WithCancel(context.Background())
	m.workspace = cancelledRequestWorkspace{fakeWorkspace: ws, cancel: cancel}
	defer cancel()
	request := httptest.NewRequestWithContext(callCtx, "POST", "/api/v1/sessions", nil)
	_, _, _, err := m.Spawn(request.Context(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn error = %v", err)
	}
	recs, err := s.ListAllSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !recs[0].IsTerminated || recs[0].Metadata.Startup == nil || !recs[0].Metadata.Startup.WorkspaceUncertain {
		t.Fatalf("cancelled HTTP request lost uncertain workspace evidence or left an active seed: %+v", recs)
	}
}

func TestStartupSQLiteJournalRecoversWithFreshManager(t *testing.T) {
	m, s, rt, ws := newDurableStartupManager(t)
	seed := seedRecord(ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}, testRoleAgents(), time.Now())
	seed.Metadata.Startup = &domain.SessionStartup{ID: "interrupted-operation", Stage: "launch_commit", RuntimePossible: true}
	seed.Metadata.RuntimeHandleID = "owned-handle"
	seed.Metadata.RuntimeLaunchID = "owned-launch"
	seed.Metadata.WorkspacePath = "/recorded-workspace"
	seed.Metadata.Branch = "recorded-branch"
	rec, err := s.CreateSession(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.destroyed != 1 || rt.destroyedIDs[0] != "owned-handle" || ws.destroyed != 1 {
		t.Fatalf("recovery counts runtime=%v workspace=%d", rt.destroyedIDs, ws.destroyed)
	}
	if after, ok, err := s.GetSession(context.Background(), rec.ID); err != nil || ok {
		t.Fatalf("recovered row remains: %+v exists=%v err=%v", after, ok, err)
	}
}

func TestStartupSQLiteCASRejectsFinishedOrReplacedOperation(t *testing.T) {
	m, s, _, _ := newDurableStartupManager(t)
	rec, a, err := m.createSpawnSeed(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}, testRoleAgents())
	if err != nil {
		t.Fatal(err)
	}
	defer m.endAgentOperation(rec.ID, agentOperationSpawn)
	replacement := rec
	replacement.Metadata.Startup = nil
	replacement.Metadata.RuntimeHandleID = "new-runtime"
	replacement.Metadata.RuntimeLaunchID = "new-launch"
	if err := s.CommitSessionSpawn(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	applied, err := s.UpdateSessionStartup(context.Background(), rec, a.fact.ID, rec.ControllerOwner())
	if err != nil || applied {
		t.Fatalf("stale startup update applied=%v err=%v", applied, err)
	}
	after, _, err := s.GetSession(context.Background(), rec.ID)
	if err != nil || after.Metadata.RuntimeHandleID != "new-runtime" || after.Metadata.Startup != nil {
		t.Fatalf("replacement overwritten: %+v err=%v", after, err)
	}
}

type failingChatStop struct {
	*recordingLauncher
	stopErr error
}

func (l *failingChatStop) StopChat(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.stopErr != nil {
		return l.stopErr
	}
	return l.recordingLauncher.StopChat(ctx, id)
}
func TestSpawnChatCleanupFailureRetainsControllerAndWorkspace(t *testing.T) {
	stopErr := errors.New("controller shutdown unconfirmed")
	launcher := &failingChatStop{recordingLauncher: &recordingLauncher{turnErr: context.Canceled}, stopErr: stopErr}
	m, st, _ := newChatManager(launcher)
	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, RequestedMode: domain.SessionModeChat, Prompt: "begin"})
	if !errors.Is(err, stopErr) {
		t.Fatalf("cleanup failure hidden: %v", err)
	}
	rec := st.sessions["mer-1"]
	if rec.IsTerminated || rec.Metadata.Startup == nil || !rec.Metadata.Startup.ControllerPossible || rec.Metadata.WorkspacePath == "" {
		t.Fatalf("uncertain controller ownership lost: %+v", rec)
	}
	launcher.stopErr = nil
	if _, err := m.Kill(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	after := st.sessions[rec.ID]
	if !after.IsTerminated || after.Metadata.Startup != nil || after.Metadata.WorkspacePath != "" {
		t.Fatalf("retry did not finish teardown: %+v", after)
	}
}

type startupCompletionWriteFailure struct{ *fakeStore }

func (s startupCompletionWriteFailure) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	if rec.Metadata.Startup == nil {
		return errors.New("completion storage unavailable")
	}
	return s.fakeStore.UpdateSession(ctx, rec)
}
func TestStartupCompletionWriteFailureDoesNotDestroyAcceptedWork(t *testing.T) {
	m, st, rt, ws := newManager()
	m.store = startupCompletionWriteFailure{st}
	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Prompt: "begin"})
	if err == nil {
		t.Fatal("missing completion write error")
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("completion persistence failure destroyed accepted work")
	}
	if rec := st.sessions["mer-1"]; rec.IsTerminated || rec.Metadata.Startup == nil || !rec.Metadata.Startup.Committed {
		t.Fatalf("committed recovery facts lost: %+v", rec)
	}
	m.store = st
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("restart destroyed accepted work without completion acknowledgement")
	}
	if rec := st.sessions["mer-1"]; rec.Metadata.Startup.Stage != "completion_unknown" || rec.Metadata.Startup.LastError == "" {
		t.Fatalf("completion uncertainty not recorded: %+v", rec.Metadata.Startup)
	}
}

func TestSpawnUntypedRuntimeErrorPreservesUncertainResources(t *testing.T) {
	m, st, rt, ws := newManager()
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.runtime = &cancelledRuntimeCreate{fakeRuntime: rt, cancel: cancel, err: context.Canceled}
	_, _, _, err := m.Spawn(callCtx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn error=%v", err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("uncertain create error led to destructive cleanup")
	}
	rec := st.sessions["mer-1"]
	if rec.IsTerminated || rec.Metadata.Startup == nil || !rec.Metadata.Startup.RuntimePossible || rec.Metadata.Startup.Stage != "cleanup_pending" {
		t.Fatalf("uncertain ownership lost: %+v", rec)
	}
}

type cancelStartupCommit struct {
	*fakeLCM
	cancel context.CancelFunc
}

func (l cancelStartupCommit) MarkSpawned(ctx context.Context, _ domain.SessionID, _ domain.SessionMetadata) error {
	l.cancel()
	return ctx.Err()
}
func TestSpawnCancelledLaunchCommitStopsRuntimeBeforeWorkspace(t *testing.T) {
	m, st, rt, ws := newManager()
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.store = cancellationAwareStartupStore{st}
	m.lcm = cancelStartupCommit{fakeLCM: &fakeLCM{store: st}, cancel: cancel}
	rt.onDestroy = func(_ int, _ ports.RuntimeHandle) {
		if ws.destroyed != 0 {
			t.Fatal("workspace removed before runtime stopped")
		}
	}
	_, _, _, err := m.Spawn(callCtx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn error=%v", err)
	}
	if rt.destroyed != 1 || ws.destroyed != 1 {
		t.Fatalf("cleanup counts runtime=%d workspace=%d", rt.destroyed, ws.destroyed)
	}
	if rec := st.sessions["mer-1"]; !rec.IsTerminated || rec.Metadata.Startup != nil || rec.Metadata.RuntimeHandleID != "" || rec.Metadata.WorkspacePath != "" {
		t.Fatalf("cancelled commit left inconsistent row: %+v", rec)
	}
}

func TestStartupRecoveryKeepsUnknownWorkspaceCreationForInspection(t *testing.T) {
	m, st, rt, ws := newManager()
	rec := seedRecord(ports.SpawnConfig{ProjectID: "mer"}, testRoleAgents(), time.Now())
	rec.ID = "mer-1"
	rec.Metadata.Startup = &domain.SessionStartup{ID: "interrupted-workspace", Stage: "workspace_creating"}
	st.sessions[rec.ID] = rec
	for range 2 {
		if err := m.reconcileStartups(context.Background()); err != nil {
			t.Fatal(err)
		}
		after, exists := st.sessions[rec.ID]
		if !exists || !after.IsTerminated || after.Metadata.Startup == nil || !after.Metadata.Startup.WorkspaceUncertain {
			t.Fatalf("unknown workspace evidence lost: %+v exists=%v", after, exists)
		}
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("unknown ownership caused destructive cleanup")
	}
}

func TestStartupRecoveryRejectsMissingOperationIdentity(t *testing.T) {
	m, st, rt, ws := newManager()
	rec := mkLive("mer-1")
	rec.Metadata.Startup = &domain.SessionStartup{Stage: "cleanup_pending", LastError: "invalid startup operation record"}
	st.sessions[rec.ID] = rec
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 || st.sessions[rec.ID].IsTerminated {
		t.Fatal("invalid startup journal authorized destructive recovery")
	}
}

func (l *failingChatStop) StopChatStartup(ctx context.Context, id domain.SessionID, _ string) (bool, error) {
	err := l.StopChat(ctx, id)
	return err == nil, err
}

type changedStartupOwnerStore struct {
	*fakeStore
	reads int
}

func (s *changedStartupOwnerStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.reads++
	if s.reads == 2 {
		rec := s.sessions[id]
		rec.Metadata.Startup = nil
		rec.Metadata.RuntimeLaunchID = "replacement-launch"
		rec.Metadata.RuntimeHandleID = "replacement-handle"
		s.sessions[id] = rec
	}
	return s.fakeStore.GetSession(ctx, id)
}
func TestStartupCleanupStopsWhenOwnershipChangesBeforePersistence(t *testing.T) {
	m, st, rt, ws := newManager()
	rec := mkLive("mer-1")
	rec.Metadata.Startup = &domain.SessionStartup{ID: "old-operation", Stage: "launch_commit", RuntimePossible: true}
	st.sessions[rec.ID] = rec
	m.store = &changedStartupOwnerStore{fakeStore: st}
	if err := m.cleanupStartup(context.Background(), startupAttemptFromRecord(rec), errors.New("failed start")); err == nil {
		t.Fatal("changed owner was accepted")
	}
	if rt.destroyed != 0 || ws.destroyed != 0 {
		t.Fatal("ownership loss during persistence did not stop destructive cleanup")
	}
}

func TestStartupKillPreservesWorkspaceWhenAuxiliaryTeardownFails(t *testing.T) {
	for _, resource := range []string{"reviewer", "shell"} {
		t.Run(resource, func(t *testing.T) {
			m, st, rt, ws := newManager()
			rec := mkLive("mer-1")
			rec.Metadata.Startup = &domain.SessionStartup{ID: "committed-operation", Stage: "completion_unknown", RuntimePossible: true, Committed: true}
			st.sessions[rec.ID] = rec
			stopErr := errors.New("auxiliary process remains alive")
			if resource == "reviewer" {
				m.SetReviewerTerminator(&fakeReviewerTerminator{err: stopErr})
			} else {
				m.SetShellTerminalCloser(&fakeShellTerminalCloser{err: stopErr})
			}
			_, err := m.Kill(context.Background(), rec.ID)
			if !errors.Is(err, stopErr) {
				t.Fatalf("auxiliary stop error = %v", err)
			}
			if ws.destroyed != 0 || rt.destroyed != 0 {
				t.Fatal("auxiliary failure crossed destructive cleanup barrier")
			}
			if after := st.sessions[rec.ID]; after.IsTerminated || after.Metadata.Startup == nil || after.Metadata.WorkspacePath == "" {
				t.Fatalf("retry evidence lost: %+v", after)
			}
		})
	}
}

type missingStartupController struct{ *recordingLauncher }

func (l missingStartupController) StopChatStartup(context.Context, domain.SessionID, string) (bool, error) {
	return false, nil
}
func TestStartupMissingChatRegistryCannotAuthorizeWorkspaceRemoval(t *testing.T) {
	m, st, rt := newChatManager(missingStartupController{&recordingLauncher{}})
	ws := &fakeWorkspace{path: "/workspace"}
	m.workspace = ws
	rec := mkLive("mer-1")
	rec.Mode = domain.SessionModeChat
	rec.Metadata.RuntimeHandleID = ""
	rec.Metadata.RuntimeLaunchID = ""
	rec.Metadata.ControllerGeneration = "before-restart"
	rec.Metadata.Startup = &domain.SessionStartup{ID: "interrupted-provider", Stage: "cleanup_pending", ControllerPossible: true, ControllerGeneration: "before-restart"}
	st.sessions[rec.ID] = rec
	if err := m.reconcileStartups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 0 || rt.destroyed != 0 {
		t.Fatal("empty provider registry authorized workspace deletion")
	}
	if after := st.sessions[rec.ID]; after.IsTerminated || after.Metadata.Startup == nil || !after.Metadata.Startup.ControllerPossible {
		t.Fatalf("provider ownership evidence lost: %+v", after)
	}
}

func TestStartupSQLiteCASRejectsDifferentLaunchWithSameOperation(t *testing.T) {
	m, s, _, _ := newDurableStartupManager(t)
	rec, a, err := m.createSpawnSeed(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}, testRoleAgents())
	if err != nil {
		t.Fatal(err)
	}
	defer m.endAgentOperation(rec.ID, agentOperationSpawn)
	replacement := rec
	replacement.Metadata.RuntimeLaunchID = "new-launch"
	if err := s.UpdateSession(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	applied, err := s.UpdateSessionStartup(context.Background(), rec, a.fact.ID, rec.ControllerOwner())
	if err != nil || applied {
		t.Fatalf("stale generation update applied=%v error=%v", applied, err)
	}
}

type partialCancelledWorkspaceProject struct {
	*fakeWorkspace
	cancel            context.CancelFunc
	cleanupContextErr error
}

func (w *partialCancelledWorkspaceProject) CreateWorkspaceProject(ctx context.Context, cfg ports.WorkspaceProjectConfig) (ports.WorkspaceProjectInfo, error) {
	info, err := w.fakeWorkspace.CreateWorkspaceProject(ctx, cfg)
	if err != nil {
		return info, err
	}
	w.cancel()
	return info, context.Canceled
}
func (w *partialCancelledWorkspaceProject) DestroyWorkspaceProject(ctx context.Context, info ports.WorkspaceProjectInfo) error {
	w.cleanupContextErr = ctx.Err()
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.fakeWorkspace.DestroyWorkspaceProject(ctx, info)
}
func TestSpawnCancelledWorkspaceProjectRollsBackReturnedResources(t *testing.T) {
	m, st, rt, ws := newManager()
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	project.Path = t.TempDir()
	st.projects["mer"] = project
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace := &partialCancelledWorkspaceProject{fakeWorkspace: ws, cancel: cancel}
	m.workspace = workspace
	_, _, _, err := m.Spawn(callCtx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn error=%v", err)
	}
	if rt.created != 0 || ws.projectDestroyed != 1 || workspace.cleanupContextErr != nil {
		t.Fatalf("partial cleanup runtime=%d project=%d context=%v", rt.created, ws.projectDestroyed, workspace.cleanupContextErr)
	}
	if _, exists := st.sessions["mer-1"]; exists {
		t.Fatal("confirmed partial workspace cleanup retained seed")
	}
}

type cancelledRequestWorkspace struct {
	*fakeWorkspace
	cancel context.CancelFunc
}

func (w cancelledRequestWorkspace) Create(ctx context.Context, _ ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.cancel()
	return ports.WorkspaceInfo{}, ctx.Err()
}
