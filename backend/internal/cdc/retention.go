package cdc

import (
	"context"
	"log/slog"
	"time"
)

const (
	// DefaultRetention keeps enough history for normal SSE reconnects without
	// allowing the durable feed to grow forever.
	DefaultRetention = 7 * 24 * time.Hour
	// DefaultRetentionInterval is long enough to avoid competing with normal
	// writes while still recovering a database that already exceeded the cap.
	DefaultRetentionInterval = 15 * time.Minute
)

const (
	// DefaultMaxRows is a second bound for installations producing events faster
	// than the age window. The cap is intentionally generous for replay while
	// keeping the SQLite table bounded under activity bursts.
	DefaultMaxRows int64 = 100_000
	// DefaultRetentionBatch bounds each delete transaction so cleanup cannot
	// monopolize SQLite's single writer connection.
	DefaultRetentionBatch         int64 = 10_000
	defaultRetentionBatchesPerRun int   = 8
)

// RetentionStore is the small storage surface needed by the change-log
// janitor. Keeping it here makes cleanup testable without a real database.
type RetentionStore interface {
	PruneChangeLogBefore(context.Context, time.Time, int64) (int64, error)
	PruneChangeLogToMaxRows(context.Context, int64, int64) (int64, error)
}

// RetentionConfig customizes change-log cleanup. Zero values use the safe
// production defaults.
type RetentionConfig struct {
	Retention time.Duration
	MaxRows   int64
	Interval  time.Duration
	Batch     int64
	Clock     func() time.Time
	Logger    *slog.Logger
}

// RetentionJanitor bounds the durable CDC log by age and row count. It runs in
// the background so writes and daemon startup never wait for a large cleanup.
type RetentionJanitor struct {
	store      RetentionStore
	retention  time.Duration
	maxRows    int64
	interval   time.Duration
	batch      int64
	clock      func() time.Time
	logger     *slog.Logger
	maxBatches int
}

// NewRetentionJanitor constructs a bounded change-log cleanup worker.
func NewRetentionJanitor(store RetentionStore, cfg RetentionConfig) *RetentionJanitor {
	j := &RetentionJanitor{
		store:      store,
		retention:  cfg.Retention,
		maxRows:    cfg.MaxRows,
		interval:   cfg.Interval,
		batch:      cfg.Batch,
		clock:      cfg.Clock,
		logger:     cfg.Logger,
		maxBatches: defaultRetentionBatchesPerRun,
	}
	if j.retention <= 0 {
		j.retention = DefaultRetention
	}
	if j.maxRows <= 0 {
		j.maxRows = DefaultMaxRows
	}
	if j.interval <= 0 {
		j.interval = DefaultRetentionInterval
	}
	if j.batch <= 0 {
		j.batch = DefaultRetentionBatch
	}
	if j.clock == nil {
		j.clock = time.Now
	}
	if j.logger == nil {
		j.logger = slog.Default()
	}
	return j
}

// Start launches cleanup immediately and then at the configured interval. The
// returned channel closes after cancellation and is safe to wait on before
// closing the store.
func (j *RetentionJanitor) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		j.run(ctx)
		t := time.NewTicker(j.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				j.run(ctx)
			}
		}
	}()
	return done
}

// RunOnce performs bounded cleanup and is exported for deterministic tests and
// for callers that want to trigger maintenance during startup.
func (j *RetentionJanitor) RunOnce(ctx context.Context) error {
	before := j.clock().UTC().Add(-j.retention)
	var total int64
	for i := 0; i < j.maxBatches; i++ {
		removedByAge, err := j.store.PruneChangeLogBefore(ctx, before, j.batch)
		if err != nil {
			return err
		}
		removedBySize, err := j.store.PruneChangeLogToMaxRows(ctx, j.maxRows, j.batch)
		if err != nil {
			return err
		}
		removed := removedByAge + removedBySize
		total += removed
		if removed == 0 {
			break
		}
	}
	if total > 0 {
		j.logger.Info("cdc change_log retention pruned rows", "rows", total)
	}
	return nil
}

func (j *RetentionJanitor) run(ctx context.Context) {
	if err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
		j.logger.Warn("cdc change_log retention failed", "err", err)
	}
}
