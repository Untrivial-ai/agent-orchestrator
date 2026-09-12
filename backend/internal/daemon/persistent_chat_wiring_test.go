package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPersistentChatHostKeepSetUsesDurableOwnership(t *testing.T) {
	records := []domain.SessionRecord{
		{ID: "live-chat", Mode: domain.SessionModeChat, Harness: domain.HarnessCodex},
		{ID: "terminated-chat", Mode: domain.SessionModeChat, Harness: domain.HarnessCodex, IsTerminated: true},
		{ID: "tui", Mode: domain.SessionModeTUI, Harness: domain.HarnessCodex},
		{ID: "other-provider", Mode: domain.SessionModeChat, Harness: domain.HarnessClaudeCode},
	}
	keep := persistentChatHostKeepSet(records)
	if len(keep) != 2 {
		t.Fatalf("keep = %v, want both live Chat providers", keep)
	}
	if _, ok := keep["live-chat"]; !ok {
		t.Fatalf("keep = %v, missing live-chat", keep)
	}
	if _, ok := keep["other-provider"]; !ok {
		t.Fatalf("keep = %v, missing other-provider", keep)
	}
}

type countingSessionStore struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (s *countingSessionStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return nil, errors.New("boom")
	}
	return nil, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Reconciliation used to run only once, at daemon boot, so a host orphaned
// after boot leaked until the next restart (the RAM-exhaustion incident this
// loop fixes). This confirms the loop actually keeps re-reconciling for as
// long as the daemon runs, not just once.
func TestPersistentChatHostReconcileLoop_TicksRepeatedlyAndStopsOnCancel(t *testing.T) {
	store := &countingSessionStore{}
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := t.TempDir()

	done := make(chan struct{})
	go func() {
		persistentChatHostReconcileLoop(ctx, dataDir, store, discardLogger(), 10*time.Millisecond)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for store.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("expected at least 3 reconcile ticks, got %d", store.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop after context cancellation")
	}
}

// A transient failure to list sessions (e.g. a busy store) must not abandon
// reconciliation for the rest of the daemon's uptime -- the loop keeps
// retrying on the next tick instead of exiting.
func TestPersistentChatHostReconcileLoop_SurvivesStoreErrors(t *testing.T) {
	store := &countingSessionStore{}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := t.TempDir()

	done := make(chan struct{})
	go func() {
		persistentChatHostReconcileLoop(ctx, dataDir, store, discardLogger(), 10*time.Millisecond)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for store.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("expected the loop to keep retrying after errors, got %d calls", store.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop after context cancellation")
	}
}
