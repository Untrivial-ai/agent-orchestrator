package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type fakeStore struct {
	mu             sync.Mutex
	events         []sqlitestore.TelemetryEventRecord
	pruned         int64
	pruneCallCount int
	freelistCount  int64
	vacuumCalled   bool
	vacuumPages    int64
}

func (f *fakeStore) CreateTelemetryEvent(_ context.Context, rec sqlitestore.TelemetryEventRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, rec)
	return nil
}

func (f *fakeStore) PruneTelemetryEventsBefore(_ context.Context, _ time.Time, limit int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCallCount++
	if f.pruned > 0 {
		n := f.pruned
		if n > limit {
			n = limit
		}
		f.pruned -= n
		return n, nil
	}
	return 0, nil
}

func (f *fakeStore) FreelistCount(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.freelistCount, nil
}

func (f *fakeStore) IncrementalVacuum(_ context.Context, pages int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vacuumCalled = true
	f.vacuumPages = pages
	return nil
}

func testSink(store *fakeStore) *LocalSQLiteSink {
	s := &LocalSQLiteSink{
		store:     store,
		log:       slog.Default(),
		retention: 7 * 24 * time.Hour,
		ch:        make(chan ports.TelemetryEvent, localBufferSize),
		now:       time.Now,
		newID:     func() string { return "test-id" },
		sleep:     func(time.Duration) {},
	}
	return s
}

func TestPruneDeletesAllExpiredRows(t *testing.T) {
	store := &fakeStore{pruned: 12000}
	s := testSink(store)

	s.maybePrune()

	store.mu.Lock()
	defer store.mu.Unlock()

	if store.pruned != 0 {
		t.Fatalf("expected all rows pruned, %d remaining", store.pruned)
	}
	if store.pruneCallCount < 3 {
		t.Fatalf("expected multiple prune batches, got %d calls", store.pruneCallCount)
	}
}

func TestPruneSkipsWhenCalledTooSoon(t *testing.T) {
	store := &fakeStore{pruned: 100}
	s := testSink(store)

	s.maybePrune()
	firstCount := store.pruneCallCount

	s.maybePrune()

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.pruneCallCount != firstCount {
		t.Fatalf("second prune should have been skipped (too soon), but got %d total calls", store.pruneCallCount)
	}
}

func TestVacuumRunsWhenFreelistExceedsThreshold(t *testing.T) {
	store := &fakeStore{pruned: 100, freelistCount: 500}
	s := testSink(store)

	s.maybePrune()

	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.vacuumCalled {
		t.Fatal("vacuum should have run when freelist exceeded threshold")
	}
	if store.vacuumPages != localVacuumPages {
		t.Fatalf("vacuum pages = %d, want %d", store.vacuumPages, localVacuumPages)
	}
}

func TestVacuumSkippedWhenFreelistBelowThreshold(t *testing.T) {
	store := &fakeStore{pruned: 100, freelistCount: 10}
	s := testSink(store)

	s.maybePrune()

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.vacuumCalled {
		t.Fatal("vacuum should not run when freelist is below threshold")
	}
}

func TestRetentionFloorApplied(t *testing.T) {
	store := &fakeStore{}
	s := NewLocalSQLiteSink(store, slog.Default(), 1*time.Hour)

	if s.retention != DefaultRetention {
		t.Fatalf("retention = %v, want default %v when below minimum", s.retention, DefaultRetention)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.Close(ctx)
}

func TestCustomRetentionAccepted(t *testing.T) {
	store := &fakeStore{}
	s := NewLocalSQLiteSink(store, slog.Default(), 7*24*time.Hour)

	if s.retention != 7*24*time.Hour {
		t.Fatalf("retention = %v, want 7 days", s.retention)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.Close(ctx)
}
