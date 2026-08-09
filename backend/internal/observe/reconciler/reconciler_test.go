package reconciler_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/reconciler"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// --- fakes ---

type fakeFinalizer struct {
	called chan domain.SessionID
}

func (f *fakeFinalizer) FinalizeTerminalSession(_ context.Context, id domain.SessionID) error {
	f.called <- id
	return nil
}

type fakeCandidates struct {
	mu  sync.Mutex
	ids []domain.SessionID
}

func (f *fakeCandidates) ListTerminalCleanupCandidates(_ context.Context, _ time.Time) ([]domain.SessionID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.SessionID, len(f.ids))
	copy(out, f.ids)
	return out, nil
}

type fakeBroadcaster struct {
	mu sync.Mutex
	fn func(cdc.Event)
}

func (b *fakeBroadcaster) Subscribe(fn func(cdc.Event)) func() {
	b.mu.Lock()
	b.fn = fn
	b.mu.Unlock()
	return func() { b.mu.Lock(); b.fn = nil; b.mu.Unlock() }
}

func (b *fakeBroadcaster) publish(e cdc.Event) {
	b.mu.Lock()
	fn := b.fn
	b.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

func sessionUpdated(id string, terminated bool) cdc.Event {
	payload, _ := json.Marshal(map[string]any{"id": id, "isTerminated": terminated})
	return cdc.Event{Type: cdc.EventSessionUpdated, SessionID: id, Payload: payload}
}

func awaitCall(t *testing.T, ch chan domain.SessionID, want domain.SessionID) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("finalize called for %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for finalize(%q)", want)
	}
}

func expectNoCall(t *testing.T, ch chan domain.SessionID) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatalf("unexpected finalize for %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// --- unit tests (fakes) ---

func TestLiveWake_EnqueuesTerminatedSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fin := &fakeFinalizer{called: make(chan domain.SessionID, 4)}
	bcast := &fakeBroadcaster{}
	r := reconciler.New(fin, &fakeCandidates{}, bcast, reconciler.Config{})
	done := r.Start(ctx)

	bcast.publish(sessionUpdated("mer-1", true))
	awaitCall(t, fin.called, "mer-1")

	cancel()
	<-done
}

func TestLiveWake_IgnoresNonTerminalAndFactsOnlyEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fin := &fakeFinalizer{called: make(chan domain.SessionID, 4)}
	bcast := &fakeBroadcaster{}
	r := reconciler.New(fin, &fakeCandidates{}, bcast, reconciler.Config{})
	done := r.Start(ctx)

	// A live (non-terminal) session_updated must not wake the reconciler.
	bcast.publish(sessionUpdated("mer-1", false))
	// A facts-trigger event carries {id} only (no isTerminated): it is the
	// frontend refetch nudge and must NOT self-wake the reconciler (critique #14).
	bcast.publish(cdc.Event{Type: cdc.EventSessionUpdated, SessionID: "mer-2", Payload: json.RawMessage(`{"id":"mer-2"}`)})
	expectNoCall(t, fin.called)

	cancel()
	<-done
}

func TestBootScan_EnqueuesBacklogCandidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fin := &fakeFinalizer{called: make(chan domain.SessionID, 4)}
	cands := &fakeCandidates{ids: []domain.SessionID{"mer-9"}}
	r := reconciler.New(fin, cands, &fakeBroadcaster{}, reconciler.Config{})
	done := r.Start(ctx)

	awaitCall(t, fin.called, "mer-9")

	cancel()
	<-done
}

// --- wake-path e2e (real store + real CDC poller/broadcaster + reconciler) ---

// TestWakePath_E2E drives convergence through the REAL session_updated CDC chain
// (store trigger → change_log → poller → broadcaster → reconciler subscription →
// finalizer), which unit-testing the finalizer in isolation cannot cover
// (critique #6).
func TestWakePath_E2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "mer", Path: "/tmp/mer", RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Now().UTC()},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Real CDC pipe seeked to head, so only the future is_terminated flip is seen.
	bcast := cdc.NewBroadcaster()
	poller := cdc.NewPoller(store, bcast, cdc.PollerConfig{})
	if err := poller.SeekToHead(ctx); err != nil {
		t.Fatalf("seek: %v", err)
	}
	pollerDone := poller.Start(ctx)

	fin := &fakeFinalizer{called: make(chan domain.SessionID, 4)}
	r := reconciler.New(fin, store, bcast, reconciler.Config{})
	reconcilerDone := r.Start(ctx)

	// Flip is_terminated: the sessions_cdc_update trigger fans out a
	// session_updated event carrying isTerminated=true.
	rec.IsTerminated = true
	if err := store.UpdateSession(ctx, rec); err != nil {
		t.Fatalf("mark terminated: %v", err)
	}

	awaitCall(t, fin.called, rec.ID)

	cancel()
	<-reconcilerDone
	<-pollerDone
}
