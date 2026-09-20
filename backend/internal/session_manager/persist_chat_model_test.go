package sessionmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type modelUpdateBarrierStore struct {
	*sqlite.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *modelUpdateBarrierStore) pause() {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
}

func (s *modelUpdateBarrierStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, ok, err := s.Store.GetSession(ctx, id)
	if err == nil && ok {
		// The former read/modify/write implementation paused here with a stale
		// snapshot; retaining the hook makes the regression reproduce on it.
		s.pause()
	}
	return rec, ok, err
}

func (s *modelUpdateBarrierStore) UpdateSessionModel(ctx context.Context, id domain.SessionID, model string) (bool, error) {
	s.pause()
	return s.Store.UpdateSessionModel(ctx, id, model)
}

func TestPersistChatModelPreservesConcurrentLifecycleUpdate(t *testing.T) {
	testCtx := context.Background()
	base := sqlitetest.MustOpen(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := base.UpsertProject(testCtx, domain.ProjectRecord{
		ID: "repro", Path: t.TempDir(), RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := base.CreateSession(testCtx, domain.SessionRecord{
		ProjectID: "repro",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			RuntimeHandleID:        "runtime-old",
			AgentSessionID:         "native-1",
			ProviderConversationID: "provider-old",
			ControllerGeneration:   "generation-old",
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := &modelUpdateBarrierStore{
		Store: base, entered: make(chan struct{}), release: make(chan struct{}),
	}
	manager := &Manager{store: blocked}
	done := make(chan error, 1)
	go func() {
		done <- manager.PersistChatModel(testCtx, created.ID, "5.6-luna")
	}()

	select {
	case <-blocked.entered:
	case err := <-done:
		t.Fatalf("model update returned before the barrier: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("model update did not reach the barrier")
	}

	newer, ok, err := base.GetSession(testCtx, created.ID)
	if err != nil || !ok {
		t.Fatalf("read concurrent state: ok=%v err=%v", ok, err)
	}
	newer.IsTerminated = true
	newer.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: now.Add(time.Second)}
	newer.Metadata.RuntimeHandleID = ""
	newer.Metadata.ProviderConversationID = "provider-new"
	newer.Metadata.ControllerGeneration = "generation-new"
	newer.UpdatedAt = now.Add(time.Second)
	if err := base.UpdateSession(testCtx, newer); err != nil {
		t.Fatal(err)
	}

	close(blocked.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	got, ok, err := base.GetSession(testCtx, created.ID)
	if err != nil || !ok {
		t.Fatalf("read result: ok=%v err=%v", ok, err)
	}
	if got.Metadata.Model != "5.6-luna" || !got.IsTerminated ||
		got.Activity.State != domain.ActivityExited || got.Metadata.RuntimeHandleID != "" ||
		got.Metadata.ProviderConversationID != "provider-new" ||
		got.Metadata.ControllerGeneration != "generation-new" {
		t.Fatalf("model update overwrote concurrent lifecycle state: %+v", got)
	}
}
