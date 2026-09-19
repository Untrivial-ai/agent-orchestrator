package usage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCoordinatorDrainsAdditionalChunksImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	store := &coordinatorTestStore{
		sources: []domain.UsageSourceRecord{watchableTestSource(1, path)},
	}
	watcher := newCoordinatorTestWatcher()
	var calls atomic.Int64
	called := make(chan struct{}, 2)
	ingestor := coordinatorTestIngestor(func(context.Context, int64) (IngestResult, error) {
		count := calls.Add(1)
		called <- struct{}{}
		return IngestResult{More: count == 1}, nil
	})

	cancel, done := startCoordinatorTest(t, store, ingestor, watcher, CoordinatorConfig{Workers: 1})
	waitForCoordinatorCalls(t, called, 2)
	stopCoordinatorTest(t, cancel, done)

	if got := calls.Load(); got != 2 {
		t.Fatalf("ingestion calls = %d, want 2", got)
	}
}

func TestCoordinatorRequeuesEventArrivingDuringIngestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := &coordinatorTestStore{
		sources: []domain.UsageSourceRecord{watchableTestSource(2, path)},
	}
	watcher := newCoordinatorTestWatcher()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondCalled := make(chan struct{})
	var calls atomic.Int64
	ingestor := coordinatorTestIngestor(func(ctx context.Context, _ int64) (IngestResult, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		case 2:
			close(secondCalled)
		}
		return IngestResult{}, nil
	})

	cancel, done := startCoordinatorTest(t, store, ingestor, watcher, CoordinatorConfig{Workers: 1})
	waitForCoordinatorSignal(t, firstStarted, "first ingestion did not start")
	watcher.events <- TranscriptEvent{Path: path}
	close(releaseFirst)
	waitForCoordinatorSignal(t, secondCalled, "event received during ingestion was dropped")
	stopCoordinatorTest(t, cancel, done)

	if got := calls.Load(); got != 2 {
		t.Fatalf("ingestion calls = %d, want 2", got)
	}
}

func TestCoordinatorFilesystemEventPreemptsPersistedRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	retryAt := time.Now().UTC().Add(time.Hour)
	source := watchableTestSource(3, path)
	source.State = domain.UsageSourceError
	source.NextRetryAt = &retryAt
	store := &coordinatorTestStore{sources: []domain.UsageSourceRecord{source}}
	watcher := newCoordinatorTestWatcher()
	called := make(chan struct{}, 1)
	ingestor := coordinatorTestIngestor(func(context.Context, int64) (IngestResult, error) {
		called <- struct{}{}
		return IngestResult{}, nil
	})

	cancel, done := startCoordinatorTest(t, store, ingestor, watcher, CoordinatorConfig{Workers: 1})
	select {
	case <-called:
		t.Fatal("source was ingested before its persisted retry deadline")
	case <-time.After(100 * time.Millisecond):
	}
	watcher.events <- TranscriptEvent{Path: path}
	waitForCoordinatorCalls(t, called, 1)
	stopCoordinatorTest(t, cancel, done)
}

func TestCoordinatorReconcilesAfterMissingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.jsonl")
	store := &coordinatorTestStore{
		sources: []domain.UsageSourceRecord{watchableTestSource(4, path)},
	}
	watcher := newCoordinatorTestWatcher()
	var reconciles atomic.Int64
	reconciledAfterIngest := make(chan struct{}, 1)
	ingestor := coordinatorTestIngestor(func(context.Context, int64) (IngestResult, error) {
		return IngestResult{
			Reconcile: true,
			RetryAt:   timePointer(time.Now().UTC().Add(time.Minute)),
		}, errors.New("artifact missing")
	})
	cfg := CoordinatorConfig{
		Workers: 1,
		Reconcile: func(context.Context) error {
			if reconciles.Add(1) == 2 {
				reconciledAfterIngest <- struct{}{}
			}
			return nil
		},
	}

	cancel, done := startCoordinatorTest(t, store, ingestor, watcher, cfg)
	waitForCoordinatorCalls(t, reconciledAfterIngest, 1)
	stopCoordinatorTest(t, cancel, done)

	if got := reconciles.Load(); got < 2 {
		t.Fatalf("reconciliation calls = %d, want at least startup plus missing-source recovery", got)
	}
}

func TestCoordinatorRebuildsWatcherAndReconcilesAfterWatcherError(t *testing.T) {
	store := &coordinatorTestStore{}
	watcher := newCoordinatorTestWatcher()
	var reconciles atomic.Int64
	reconciled := make(chan struct{}, 1)
	cfg := CoordinatorConfig{
		Workers: 1,
		Reconcile: func(context.Context) error {
			if reconciles.Add(1) == 2 {
				reconciled <- struct{}{}
			}
			return nil
		},
	}

	cancel, done := startCoordinatorTest(
		t,
		store,
		coordinatorTestIngestor(func(context.Context, int64) (IngestResult, error) {
			return IngestResult{}, nil
		}),
		watcher,
		cfg,
	)
	watcher.errors <- errors.New("fsnotify overflow")
	waitForCoordinatorCalls(t, reconciled, 1)
	stopCoordinatorTest(t, cancel, done)

	if got := watcher.rebuilds.Load(); got != 2 {
		t.Fatalf("watcher rebuilds = %d, want startup plus overflow recovery", got)
	}
}

func TestCoordinatorRunsDiscoveryOnlyForDiscoveryEvents(t *testing.T) {
	store := &coordinatorTestStore{}
	watcher := newCoordinatorTestWatcher()
	var initializations atomic.Int64
	var reconciles atomic.Int64
	reconciled := make(chan struct{}, 2)
	cfg := CoordinatorConfig{
		Workers: 1,
		Initialize: func(context.Context) error {
			initializations.Add(1)
			return nil
		},
		Reconcile: func(context.Context) error {
			reconciles.Add(1)
			reconciled <- struct{}{}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewCoordinator(
		store,
		coordinatorTestIngestor(func(context.Context, int64) (IngestResult, error) {
			return IngestResult{}, nil
		}),
		watcher,
		cfg,
	)
	done := coordinator.Start(ctx)
	waitForCoordinatorCalls(t, reconciled, 1)

	unknownPath := filepath.Join(t.TempDir(), "unknown.jsonl")
	watcher.events <- TranscriptEvent{Path: unknownPath}
	time.Sleep(100 * time.Millisecond)
	if got := reconciles.Load(); got != 1 {
		t.Fatalf("ordinary writes for dormant sources ran discovery %d times, want startup only", got)
	}
	coordinator.NotifyInventoryChanged()
	time.Sleep(100 * time.Millisecond)
	if got := reconciles.Load(); got != 1 {
		t.Fatalf("inventory refreshes ran discovery %d times, want startup only", got)
	}

	watcher.events <- TranscriptEvent{
		Path:      filepath.Join(t.TempDir(), "created-directory"),
		Discovery: true,
	}
	waitForCoordinatorCalls(t, reconciled, 1)
	cancel()
	waitForCoordinatorSignal(t, done, "coordinator did not stop")
	if got := reconciles.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want startup plus create", got)
	}
	if got := initializations.Load(); got != 1 {
		t.Fatalf("initialization calls = %d, want once", got)
	}
}

func TestCoordinatorStopsWorkersWhenWatcherCloses(t *testing.T) {
	store := &coordinatorTestStore{}
	for id := int64(1); id <= 32; id++ {
		store.sources = append(store.sources, watchableTestSource(id, filepath.Join(t.TempDir(), "source.jsonl")))
	}
	watcher := newClosedCoordinatorTestWatcher()
	ingestor := coordinatorTestIngestor(func(ctx context.Context, _ int64) (IngestResult, error) {
		<-ctx.Done()
		return IngestResult{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := NewCoordinator(store, ingestor, watcher, CoordinatorConfig{Workers: 1}).Start(ctx)
	waitForCoordinatorSignal(t, done, "coordinator did not stop after watcher closure")
}

type coordinatorTestStore struct {
	mu      sync.Mutex
	sources []domain.UsageSourceRecord
}

func (s *coordinatorTestStore) ListWatchableUsageSources(context.Context) ([]domain.UsageSourceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.UsageSourceRecord(nil), s.sources...), nil
}

func (s *coordinatorTestStore) HasPendingUsageDiscovery(context.Context) (bool, error) {
	return false, nil
}

type coordinatorTestIngestor func(context.Context, int64) (IngestResult, error)

func (f coordinatorTestIngestor) Ingest(ctx context.Context, sourceID int64) (IngestResult, error) {
	return f(ctx, sourceID)
}

type coordinatorTestWatcher struct {
	events   chan TranscriptEvent
	errors   chan error
	done     chan struct{}
	once     sync.Once
	rebuilds atomic.Int64
	pathsMu  sync.Mutex
	paths    []string
}

func newCoordinatorTestWatcher() *coordinatorTestWatcher {
	return &coordinatorTestWatcher{
		events: make(chan TranscriptEvent, 4),
		errors: make(chan error, 4),
		done:   make(chan struct{}),
	}
}

func (w *coordinatorTestWatcher) Events() <-chan TranscriptEvent {
	return w.events
}

func (w *coordinatorTestWatcher) Errors() <-chan error {
	return w.errors
}

func (w *coordinatorTestWatcher) Start(ctx context.Context) <-chan struct{} {
	w.once.Do(func() {
		go func() {
			defer close(w.done)
			<-ctx.Done()
		}()
	})
	return w.done
}

func (w *coordinatorTestWatcher) Rebuild(_ context.Context, paths []string) error {
	w.rebuilds.Add(1)
	w.pathsMu.Lock()
	w.paths = append([]string(nil), paths...)
	w.pathsMu.Unlock()
	return nil
}

func newClosedCoordinatorTestWatcher() *coordinatorTestWatcher {
	watcher := newCoordinatorTestWatcher()
	close(watcher.events)
	close(watcher.errors)
	close(watcher.done)
	watcher.once.Do(func() {})
	return watcher
}

func watchableTestSource(id int64, path string) domain.UsageSourceRecord {
	return domain.UsageSourceRecord{
		ID:           id,
		ArtifactPath: path,
		State:        domain.UsageSourcePending,
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func startCoordinatorTest(
	t *testing.T,
	store coordinatorStore,
	ingestor sourceIngestor,
	watcher transcriptWatcher,
	cfg CoordinatorConfig,
) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := NewCoordinator(store, ingestor, watcher, cfg).Start(ctx)
	return cancel, done
}

func stopCoordinatorTest(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	waitForCoordinatorSignal(t, done, "coordinator did not stop")
}

func waitForCoordinatorCalls(t *testing.T, called <-chan struct{}, count int) {
	t.Helper()
	for range count {
		waitForCoordinatorSignal(t, called, "timed out waiting for coordinator work")
	}
}

func waitForCoordinatorSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}
}
