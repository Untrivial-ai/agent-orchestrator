package cue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/cue"
)

// fakeStore is an in-memory cue.Store for service tests. It keeps stored
// definitions by id and lets tests inject the sentinel errors the real store
// produces.
type fakeStore struct {
	cues      map[domain.CueID]domain.Cue
	setName   func(domain.Cue) error
	selectErr func() error
}

func newFakeStore() *fakeStore {
	return &fakeStore{cues: map[domain.CueID]domain.Cue{}}
}

func (f *fakeStore) InsertCue(_ context.Context, cue domain.Cue) error {
	if f.setName != nil {
		if err := f.setName(cue); err != nil {
			return err
		}
	}
	if _, exists := f.cues[cue.ID]; exists {
		return domain.ErrCueNameExists
	}
	f.cues[cue.ID] = cue
	return nil
}

func (f *fakeStore) SelectCueByID(_ context.Context, cueID domain.CueID) (domain.Cue, bool, error) {
	if f.selectErr != nil {
		return domain.Cue{}, false, f.selectErr()
	}
	cue, ok := f.cues[cueID]
	return cue, ok, nil
}

func (f *fakeStore) SelectCuesByProject(_ context.Context, projectID domain.ProjectID) ([]domain.Cue, error) {
	var out []domain.Cue
	for _, c := range f.cues {
		if c.ProjectID == projectID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateCue(_ context.Context, cue domain.Cue) (domain.Cue, bool, error) {
	existing, ok := f.cues[cue.ID]
	if !ok {
		return domain.Cue{}, false, nil
	}
	if f.setName != nil {
		if err := f.setName(cue); err != nil {
			return domain.Cue{}, false, err
		}
	}
	for otherID, other := range f.cues {
		if otherID != cue.ID && other.Name == cue.Name && other.ProjectID == existing.ProjectID {
			return domain.Cue{}, false, domain.ErrCueNameExists
		}
	}
	cue.CreatedAt = existing.CreatedAt
	f.cues[cue.ID] = cue
	return cue, true, nil
}

func (f *fakeStore) DeleteCueByID(_ context.Context, cueID domain.CueID) (bool, error) {
	if _, ok := f.cues[cueID]; !ok {
		return false, nil
	}
	delete(f.cues, cueID)
	return true, nil
}

func newTestService(store *fakeStore) *cue.Service {
	var seq int
	return cue.New(cue.Deps{
		Store: store,
		NewID: func() string { seq++; return "cue-" + string(rune('a'+seq-1)) },
		Now:   func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) },
	})
}

func commandInput(name string) cue.Input {
	return cue.Input{Name: name, Description: "d", Type: domain.CueTypeCommand, Command: "pnpm test"}
}

// wantCode asserts err is an *apierr.Error carrying the given kind and machine
// code.
func wantCode(t *testing.T, err error, kind apierr.Kind, code string) {
	t.Helper()
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code {
		t.Fatalf("error = %v, want *apierr.Error kind=%v code=%s", err, kind, code)
	}
}

func TestCreateHappyPath(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	got, err := svc.Create(context.Background(), "mer", commandInput(" Test "))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.ID != "cue-a" || got.ProjectID != "mer" || got.Name != "Test" ||
		got.Type != domain.CueTypeCommand || got.Command != "pnpm test" {
		t.Fatalf("created cue = %+v", got)
	}
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if !got.CreatedAt.Equal(want) || !got.UpdatedAt.Equal(want) {
		t.Fatalf("timestamps = %v/%v, want %v", got.CreatedAt, got.UpdatedAt, want)
	}
	persisted, ok := store.cues["cue-a"]
	if !ok || persisted.Name != "Test" {
		t.Fatalf("stored cue = %+v, ok=%v", persisted, ok)
	}
}

func TestCreateValidation(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	tests := []struct {
		name      string
		projectID domain.ProjectID
		input     cue.Input
		code      string
	}{
		{"missing project", "", commandInput("Test"), "INVALID_PROJECT_ID"},
		{"blank name", "mer", commandInput("   "), "INVALID_CUE_NAME"},
		{"oversized name", "mer", cue.Input{Name: string(make([]byte, 65)), Description: "", Type: domain.CueTypeCommand, Command: "x"}, "INVALID_CUE_NAME"},
		{"oversized description", "mer", cue.Input{Name: "Test", Description: string(make([]byte, 241)), Type: domain.CueTypeCommand, Command: "x"}, "INVALID_CUE_DESCRIPTION"},
		{"invalid type", "mer", cue.Input{Name: "Test", Type: "prompt", Prompt: "run"}, "INVALID_CUE_TYPE"},
		{"command without command", "mer", cue.Input{Name: "Test", Type: domain.CueTypeCommand}, "INVALID_CUE_COMMAND"},
		{"oversized command", "mer", cue.Input{Name: "Test", Type: domain.CueTypeCommand, Command: string(make([]byte, (4<<10)+1))}, "INVALID_CUE_COMMAND"},
		{"agent without prompt", "mer", cue.Input{Name: "Test", Type: domain.CueTypeAgent}, "INVALID_CUE_PROMPT"},
		{"oversized prompt", "mer", cue.Input{Name: "Test", Type: domain.CueTypeAgent, Prompt: string(make([]byte, (16<<10)+1))}, "INVALID_CUE_PROMPT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tc.projectID, tc.input)
			wantCode(t, err, apierr.KindInvalid, tc.code)
		})
	}
	if len(store.cues) != 0 {
		t.Fatalf("validation rejected creation, but %d cue(s) were stored", len(store.cues))
	}
}

func TestCreateDuplicateName(t *testing.T) {
	store := newFakeStore()
	store.cues[domain.CueID("cue-a")] = domain.Cue{ID: "cue-a", ProjectID: "mer", Name: "Test"}
	svc := newTestService(store)

	_, err := svc.Create(context.Background(), "mer", commandInput("Test"))
	wantCode(t, err, apierr.KindConflict, "CUE_NAME_EXISTS")

	// The name may be reused in a different project.
	got, err := svc.Create(context.Background(), "ao", commandInput("Test"))
	if err != nil {
		t.Fatalf("create same name in another project: %v", err)
	}
	if got.ProjectID != "ao" {
		t.Fatalf("cue project = %s, want ao", got.ProjectID)
	}
}

func TestCreateUnknownProject(t *testing.T) {
	store := newFakeStore()
	store.setName = func(c domain.Cue) error { return domain.ErrProjectUnknown }
	svc := newTestService(store)

	_, err := svc.Create(context.Background(), "ghost", commandInput("Test"))
	wantCode(t, err, apierr.KindNotFound, "PROJECT_NOT_FOUND")
}

func TestGet(t *testing.T) {
	store := newFakeStore()
	store.cues[domain.CueID("cue-a")] = domain.Cue{ID: "cue-a", ProjectID: "mer", Name: "Test", Type: domain.CueTypeAgent, Prompt: "run"}
	svc := newTestService(store)

	got, err := svc.Get(context.Background(), "cue-a")
	if err != nil || got.Name != "Test" {
		t.Fatalf("get = %+v, err=%v", got, err)
	}

	_, err = svc.Get(context.Background(), "cue-missing")
	wantCode(t, err, apierr.KindNotFound, "CUE_NOT_FOUND")

	_, err = svc.Get(context.Background(), "")
	wantCode(t, err, apierr.KindInvalid, "INVALID_CUE_ID")
}

func TestList(t *testing.T) {
	store := newFakeStore()
	store.cues[domain.CueID("cue-a")] = domain.Cue{ID: "cue-a", ProjectID: "mer", Name: "Test"}
	store.cues[domain.CueID("cue-b")] = domain.Cue{ID: "cue-b", ProjectID: "ao", Name: "Other"}
	svc := newTestService(store)

	listed, err := svc.List(context.Background(), "mer")
	if err != nil || len(listed) != 1 || listed[0].Name != "Test" {
		t.Fatalf("list = %+v, err=%v", listed, err)
	}

	_, err = svc.List(context.Background(), "")
	wantCode(t, err, apierr.KindInvalid, "INVALID_PROJECT_ID")
}

func TestUpdateHappyPath(t *testing.T) {
	store := newFakeStore()
	original := domain.Cue{
		ID: "cue-a", ProjectID: "mer", Name: "Test",
		Type: domain.CueTypeCommand, Command: "pnpm test",
		CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	store.cues[domain.CueID("cue-a")] = original
	svc := newTestService(store)

	got, err := svc.Update(context.Background(), "cue-a", cue.Input{
		Name: "Run Tests", Description: "longer desc", Type: domain.CueTypeAgent, Prompt: "Run tests in watch mode.",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "Run Tests" || got.Type != domain.CueTypeAgent ||
		got.Command != "" || got.Prompt != "Run tests in watch mode." {
		t.Fatalf("updated cue = %+v", got)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("created_at changed: %v, want %v", got.CreatedAt, original.CreatedAt)
	}
	wantUpdated := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if !got.UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, wantUpdated)
	}
}

func TestUpdateErrors(t *testing.T) {
	store := newFakeStore()
	store.cues[domain.CueID("cue-a")] = domain.Cue{ID: "cue-a", ProjectID: "mer", Name: "Test"}
	store.cues[domain.CueID("cue-b")] = domain.Cue{ID: "cue-b", ProjectID: "mer", Name: "Ship"}
	svc := newTestService(store)

	_, err := svc.Update(context.Background(), "cue-missing", commandInput("Fresh"))
	wantCode(t, err, apierr.KindNotFound, "CUE_NOT_FOUND")

	_, err = svc.Update(context.Background(), "cue-b", commandInput("Test"))
	wantCode(t, err, apierr.KindConflict, "CUE_NAME_EXISTS")

	_, err = svc.Update(context.Background(), "cue-a", cue.Input{Name: "Test", Description: "", Type: "prompt", Prompt: "run"})
	wantCode(t, err, apierr.KindInvalid, "INVALID_CUE_TYPE")

	_, err = svc.Update(context.Background(), "", commandInput("Test"))
	wantCode(t, err, apierr.KindInvalid, "INVALID_CUE_ID")
}

func TestDelete(t *testing.T) {
	store := newFakeStore()
	store.cues[domain.CueID("cue-a")] = domain.Cue{ID: "cue-a", ProjectID: "mer", Name: "Test"}
	svc := newTestService(store)

	if err := svc.Delete(context.Background(), ""); err == nil {
		t.Fatal("delete with empty id succeeded")
	} else {
		wantCode(t, err, apierr.KindInvalid, "INVALID_CUE_ID")
	}
	if err := svc.Delete(context.Background(), "cue-missing"); err == nil {
		t.Fatal("delete of missing cue succeeded")
	} else {
		wantCode(t, err, apierr.KindNotFound, "CUE_NOT_FOUND")
	}
	if err := svc.Delete(context.Background(), "cue-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := store.cues[domain.CueID("cue-a")]; ok {
		t.Fatal("cue-a still present after delete")
	}
}

func TestNilStoreGuard(t *testing.T) {
	svc := cue.New(cue.Deps{})
	_, err := svc.Create(context.Background(), "mer", commandInput("Test"))
	if err == nil {
		t.Fatal("create with nil store succeeded")
	}
}

// fakeSessions is an in-memory cue.Sessions double that records what was sent
// and spawned while honoring the injected error hooks.
type fakeSessions struct {
	sessions map[domain.SessionID]domain.Session
	getErr   error
	sendErr  error
	spawnErr error
	sent     []string
	spawned  []ports.SpawnConfig
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{sessions: map[domain.SessionID]domain.Session{}}
}

func (f *fakeSessions) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if f.getErr != nil {
		return domain.Session{}, f.getErr
	}
	sess, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	return sess, nil
}

func (f *fakeSessions) Send(_ context.Context, _ domain.SessionID, message string, _ *ports.SpawnAttachment) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, message)
	return nil
}

func (f *fakeSessions) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	if f.spawnErr != nil {
		return domain.Session{}, 0, 0, f.spawnErr
	}
	f.spawned = append(f.spawned, cfg)
	return domain.Session{SessionRecord: domain.SessionRecord{ID: domain.SessionID("sess-worker"), ProjectID: cfg.ProjectID}}, 1, 1, nil
}

func activeSession(id domain.SessionID, projectID domain.ProjectID, state domain.ActivityState) domain.Session {
	return domain.Session{SessionRecord: domain.SessionRecord{
		ID: id, ProjectID: projectID, Activity: domain.Activity{State: state},
	}}
}

func agentCue(id domain.CueID, projectID domain.ProjectID, prompt string) domain.Cue {
	return domain.Cue{ID: id, ProjectID: projectID, Name: "Rephrase", Type: domain.CueTypeAgent, Prompt: prompt}
}

func commandCue(id domain.CueID, projectID domain.ProjectID) domain.Cue {
	return domain.Cue{ID: id, ProjectID: projectID, Name: "Lint", Type: domain.CueTypeCommand, Command: "pnpm lint"}
}

func newInvokeService(t *testing.T, store *fakeStore, sessions *fakeSessions) *cue.Service {
	t.Helper()
	return cue.New(cue.Deps{Store: store, Sessions: sessions})
}

func TestInvokeSendsToMessageableSession(t *testing.T) {
	store := newFakeStore()
	store.cues["cue-a"] = agentCue("cue-a", "mer", "Reword the PR description.")
	sessions := newFakeSessions()
	sessions.sessions["sess-1"] = activeSession("sess-1", "mer", domain.ActivityIdle)
	svc := newInvokeService(t, store, sessions)

	got, err := svc.Invoke(context.Background(), "cue-a", "sess-1")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "sess-1" {
		t.Fatalf("invoke returned %s, want sess-1", got)
	}
	if len(sessions.sent) != 1 || sessions.sent[0] != "Reword the PR description." {
		t.Fatalf("sent = %v", sessions.sent)
	}
	if len(sessions.spawned) != 0 {
		t.Fatalf("spawned %d worker(s), want none", len(sessions.spawned))
	}
}

func TestInvokeCommandCueWrapsCommand(t *testing.T) {
	store := newFakeStore()
	store.cues["cue-a"] = commandCue("cue-a", "mer")
	sessions := newFakeSessions()
	sessions.sessions["sess-1"] = activeSession("sess-1", "mer", domain.ActivityActive)
	svc := newInvokeService(t, store, sessions)

	if _, err := svc.Invoke(context.Background(), "cue-a", "sess-1"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(sessions.sent) != 1 || sessions.sent[0] != "Run `pnpm lint` and report the output." {
		t.Fatalf("sent = %v", sessions.sent)
	}
}

func TestInvokeWithoutSessionSpawnsWorker(t *testing.T) {
	store := newFakeStore()
	store.cues["cue-a"] = agentCue("cue-a", "mer", "Bump the version.")
	sessions := newFakeSessions()
	svc := newInvokeService(t, store, sessions)

	got, err := svc.Invoke(context.Background(), "cue-a", "")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "sess-worker" {
		t.Fatalf("invoke returned %s, want sess-worker", got)
	}
	if len(sessions.sent) != 0 {
		t.Fatalf("sent %d message(s), want none", len(sessions.sent))
	}
	if len(sessions.spawned) != 1 {
		t.Fatalf("spawned %d worker(s), want 1", len(sessions.spawned))
	}
	cfg := sessions.spawned[0]
	if cfg.ProjectID != "mer" || cfg.Kind != domain.KindWorker || cfg.Prompt != "Bump the version." {
		t.Fatalf("spawn config = %+v", cfg)
	}
}

func TestInvokeStaleSessionFallsBackToSpawn(t *testing.T) {
	store := newFakeStore()
	store.cues["cue-a"] = agentCue("cue-a", "mer", "Bump the version.")
	sessions := newFakeSessions()
	sessions.getErr = errors.New("session not found")
	svc := newInvokeService(t, store, sessions)

	got, err := svc.Invoke(context.Background(), "cue-a", "sess-gone")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "sess-worker" {
		t.Fatalf("invoke returned %s, want spawned worker", got)
	}
	if len(sessions.spawned) != 1 {
		t.Fatalf("spawned %d worker(s), want 1", len(sessions.spawned))
	}
}

func TestInvokeUnmessageableSessionFallsBackToSpawn(t *testing.T) {
	tests := []struct {
		name string
		sess domain.Session
	}{
		{"terminated", domain.Session{SessionRecord: domain.SessionRecord{ID: "sess-1", ProjectID: "mer", IsTerminated: true, Activity: domain.Activity{State: domain.ActivityActive}}}},
		{"exited", activeSession("sess-1", "mer", domain.ActivityExited)},
		{"blocked", activeSession("sess-1", "mer", domain.ActivityBlocked)},
		{"wrong project", activeSession("sess-1", "ao", domain.ActivityIdle)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.cues["cue-a"] = agentCue("cue-a", "mer", "Bump the version.")
			sessions := newFakeSessions()
			sessions.sessions["sess-1"] = tc.sess
			svc := newInvokeService(t, store, sessions)

			got, err := svc.Invoke(context.Background(), "cue-a", "sess-1")
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if got != "sess-worker" {
				t.Fatalf("invoke returned %s, want spawned worker", got)
			}
			if len(sessions.sent) != 0 {
				t.Fatalf("sent %d message(s), want none", len(sessions.sent))
			}
			if len(sessions.spawned) != 1 {
				t.Fatalf("spawned %d worker(s), want 1", len(sessions.spawned))
			}
		})
	}
}

func TestInvokeErrors(t *testing.T) {
	t.Run("empty cue id", func(t *testing.T) {
		svc := newInvokeService(t, newFakeStore(), newFakeSessions())
		_, err := svc.Invoke(context.Background(), "", "sess-1")
		wantCode(t, err, apierr.KindInvalid, "INVALID_CUE_ID")
	})

	t.Run("unknown cue", func(t *testing.T) {
		svc := newInvokeService(t, newFakeStore(), newFakeSessions())
		_, err := svc.Invoke(context.Background(), "cue-x", "sess-1")
		wantCode(t, err, apierr.KindNotFound, "CUE_NOT_FOUND")
	})

	t.Run("send error propagates", func(t *testing.T) {
		store := newFakeStore()
		store.cues["cue-a"] = agentCue("cue-a", "mer", "Bump the version.")
		sessions := newFakeSessions()
		sessions.sessions["sess-1"] = activeSession("sess-1", "mer", domain.ActivityIdle)
		sessions.sendErr = errors.New("delivery failed")
		svc := newInvokeService(t, store, sessions)

		_, err := svc.Invoke(context.Background(), "cue-a", "sess-1")
		if err == nil || err.Error() != "delivery failed" {
			t.Fatalf("invoke error = %v, want delivery failed", err)
		}
	})

	t.Run("spawn error propagates", func(t *testing.T) {
		store := newFakeStore()
		store.cues["cue-a"] = agentCue("cue-a", "mer", "Bump the version.")
		sessions := newFakeSessions()
		sessions.spawnErr = errors.New("agent not installed")
		svc := newInvokeService(t, store, sessions)

		_, err := svc.Invoke(context.Background(), "cue-a", "")
		if err == nil || err.Error() != "agent not installed" {
			t.Fatalf("invoke error = %v, want agent not installed", err)
		}
	})

	t.Run("nil sessions guard", func(t *testing.T) {
		store := newFakeStore()
		svc := cue.New(cue.Deps{Store: store})
		if _, err := svc.Invoke(context.Background(), "cue-a", ""); err == nil {
			t.Fatal("invoke without sessions succeeded")
		}
	})
}
