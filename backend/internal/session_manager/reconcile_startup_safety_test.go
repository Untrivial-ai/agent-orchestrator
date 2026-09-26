package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type failingProvisionStore struct {
	*fakeStore
	setErr   error
	failIDs  map[domain.SessionID]bool
	attempts map[domain.SessionID]int
}

func (s *failingProvisionStore) SetSessionProvisionState(ctx context.Context, id domain.SessionID, state domain.SessionProvisionState, message string, now time.Time) (bool, error) {
	s.attempts[id]++
	if s.failIDs[id] && s.setErr != nil {
		return false, s.setErr
	}
	return s.fakeStore.SetSessionProvisionState(ctx, id, state, message, now)
}

func TestStartupSafetyDoesNotAbortOnInterruptedProvisionStateWrite(t *testing.T) {
	m, st, _, _ := newManager()
	for i, id := range []domain.SessionID{"mer-1", "mer-2", "mer-4"} {
		st.sessions[id] = domain.SessionRecord{
			ID: id, ProjectID: "mer", Kind: domain.KindWorker,
			Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
			CreatedAt: time.Date(2026, time.September, 20, 12+i, 0, 0, 0, time.UTC),
		}
	}
	store := &failingProvisionStore{
		fakeStore: st, setErr: errors.New("database is locked"),
		failIDs:  map[domain.SessionID]bool{"mer-1": true, "mer-4": true},
		attempts: map[domain.SessionID]int{},
	}
	m.store = store
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatalf("non-safety provision-state write blocked daemon startup: %v", err)
	}
	for _, id := range []domain.SessionID{"mer-1", "mer-2", "mer-4"} {
		if store.attempts[id] != 1 {
			t.Fatalf("startup writes for %s = %d, want 1", id, store.attempts[id])
		}
	}
	// The successful row can be retried by the user. A new row can start after
	// the listener binds; neither is an interrupted write from the old daemon.
	retrying := st.sessions["mer-2"]
	retrying.ProvisionState = domain.SessionProvisionProvisioning
	st.sessions["mer-2"] = retrying
	st.sessions["mer-3"] = domain.SessionRecord{
		ID: "mer-3", ProjectID: "mer", Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
	}
	killed := st.sessions["mer-4"]
	killed.IsTerminated = true
	st.sessions["mer-4"] = killed
	store.setErr = nil
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.attempts["mer-1"] != 2 || st.sessions["mer-1"].ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("failed startup write not retried: attempts=%d state=%q", store.attempts["mer-1"], st.sessions["mer-1"].ProvisionState)
	}
	for _, id := range []domain.SessionID{"mer-2", "mer-3", "mer-4"} {
		wantAttempts := 0
		if id != "mer-3" {
			wantAttempts = 1
		}
		if store.attempts[id] != wantAttempts {
			t.Fatalf("background pass rewrote %s: attempts=%d, want %d", id, store.attempts[id], wantAttempts)
		}
	}
	for _, id := range []domain.SessionID{"mer-2", "mer-3"} {
		if got := st.sessions[id].ProvisionState; got != domain.SessionProvisionProvisioning {
			t.Fatalf("background pass stopped live start %s: state=%q", id, got)
		}
	}
}

func TestBackgroundProvisionRetryDoesNotTouchReusedSessionID(t *testing.T) {
	m, st, _, _ := newManager()
	created := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
		CreatedAt: created,
	}
	store := &failingProvisionStore{
		fakeStore: st, setErr: errors.New("database is locked"),
		failIDs: map[domain.SessionID]bool{"mer-1": true}, attempts: map[domain.SessionID]int{},
	}
	m.store = store
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	// SQLite can reuse MAX(num)+1 after a row is deleted. This is a new task,
	// not the interrupted one whose startup write failed.
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
		CreatedAt: created.Add(time.Hour),
	}
	store.setErr = nil
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.attempts["mer-1"] != 1 || st.sessions["mer-1"].ProvisionState != domain.SessionProvisionProvisioning {
		t.Fatalf("new task was swept by an old retry: attempts=%d state=%q", store.attempts["mer-1"], st.sessions["mer-1"].ProvisionState)
	}
}

type blockingCleanupWorkspace struct {
	*fakeWorkspace
	destroyEntered chan struct{}
	releaseDestroy chan struct{}
	createEntered  chan struct{}
	releaseCreate  chan struct{}
}

func (w *blockingCleanupWorkspace) Destroy(ctx context.Context, info ports.WorkspaceInfo) error {
	w.destroyEntered <- struct{}{}
	<-w.releaseDestroy
	return w.fakeWorkspace.Destroy(ctx, info)
}

func (w *blockingCleanupWorkspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.createEntered <- struct{}{}
	<-w.releaseCreate
	return w.fakeWorkspace.Create(ctx, cfg)
}

func TestBackgroundPreparationCleanupHoldsProjectWorkspaceGate(t *testing.T) {
	m, st, _, base := newManager()
	ws := &blockingCleanupWorkspace{
		fakeWorkspace: base, destroyEntered: make(chan struct{}, 1), releaseDestroy: make(chan struct{}),
		createEntered: make(chan struct{}, 1), releaseCreate: make(chan struct{}),
	}
	m.workspace = ws
	st.num = 1 // the post-listener preparation receives mer-2, not the old mer-1
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, ProvisionState: domain.SessionProvisionProvisioning,
		CreatedAt: time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC),
		Metadata:  domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1"},
	}
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	backgroundDone := make(chan error, 1)
	go func() { backgroundDone <- m.ReconcileBackground(context.Background()) }()
	select {
	case <-ws.destroyEntered:
	case <-time.After(time.Second):
		t.Fatal("background cleanup did not reach workspace teardown")
	}
	deferred := deferredBackground(m)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil || token == "" {
		t.Fatalf("new preparation = (%q, %v)", token, err)
	}
	prepareDone := make(chan struct{})
	go func() { (*deferred)[0](); close(prepareDone) }()
	overlapped := false
	select {
	case <-ws.createEntered:
		overlapped = true
	case <-time.After(100 * time.Millisecond):
	}
	close(ws.releaseDestroy)
	if err := <-backgroundDone; err != nil {
		t.Fatal(err)
	}
	close(ws.releaseCreate)
	<-prepareDone
	if overlapped {
		t.Fatal("new preparation entered git create while interrupted cleanup still owned the same project")
	}
}

func TestBackgroundPreparationCleanupDoesNotTouchReusedSessionID(t *testing.T) {
	m, st, _, ws := newManager()
	created := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, CreatedAt: created,
		Metadata: domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/old"},
	}
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, CreatedAt: created.Add(time.Hour),
		Metadata: domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/new"},
	}
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 0 {
		t.Fatalf("background cleanup destroyed %d workspaces after ID reuse", ws.destroyed)
	}
	if rec, ok := st.sessions["mer-1"]; !ok || rec.Metadata.WorkspacePath != "/ws/new" {
		t.Fatalf("new preparation was deleted or changed: %+v, exists=%v", rec, ok)
	}
}

type blockingProjectLookupStore struct {
	*fakeStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingProjectLookupStore) GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.fakeStore.GetProject(ctx, id)
}

func TestBackgroundReconcileDoesNotFenceNewUnfinishedTasks(t *testing.T) {
	m, st, _, _ := newManager()
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := []domain.SessionID{"mer-1", "mer-2"}
	st.sessions[ids[0]] = domain.SessionRecord{
		ID: ids[0], ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, ProvisionState: domain.SessionProvisionProvisioning,
	}
	st.sessions[ids[1]] = domain.SessionRecord{
		ID: ids[1], ProjectID: "mer", Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
	}
	lookup := &blockingProjectLookupStore{fakeStore: st, entered: make(chan struct{}, 1), release: make(chan struct{})}
	m.store = lookup
	done := make(chan error, 1)
	go func() { done <- m.ReconcileBackground(context.Background()) }()
	select {
	case <-lookup.entered:
		fenced := m.SessionMutationInProgress(ids[0]) || m.SessionMutationInProgress(ids[1])
		close(lookup.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if fenced {
			t.Fatal("background reconciliation input-gated a new preparation or provisioning Chat task")
		}
		t.Fatal("background reconciliation inspected an unfinished task as a live session")
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(lookup.release)
		t.Fatal("background reconciliation stalled on an unfinished task")
	}
	for _, id := range ids {
		if m.SessionMutationInProgress(id) {
			t.Fatalf("background reconciliation left %s input-gated", id)
		}
	}
}
