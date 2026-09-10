package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

const (
	localBufferSize      = 128
	localPruneEvery      = time.Hour
	localPruneBatchLimit = int64(5000)
	localPruneBatchPause = 50 * time.Millisecond
	localVacuumThreshold = int64(256)
	localVacuumPages     = int64(256)
)

// DefaultRetention is the default local telemetry retention period.
const DefaultRetention = 30 * 24 * time.Hour

// MinRetention is the shortest allowed retention period.
const MinRetention = 24 * time.Hour

type localStore interface {
	CreateTelemetryEvent(ctx context.Context, rec sqlitestore.TelemetryEventRecord) error
	PruneTelemetryEventsBefore(ctx context.Context, before time.Time, limit int64) (int64, error)
	FreelistCount(ctx context.Context) (int64, error)
	IncrementalVacuum(ctx context.Context, pages int64) error
}

// LocalSQLiteSink persists telemetry events into the daemon's SQLite database
// behind a small buffered worker so event emission stays best-effort.
type LocalSQLiteSink struct {
	store     localStore
	log       *slog.Logger
	retention time.Duration
	ch        chan ports.TelemetryEvent
	wg        sync.WaitGroup
	closeOnce sync.Once
	now       func() time.Time
	newID     func() string
	sleep     func(time.Duration)

	pruneMu   sync.Mutex
	lastPrune time.Time
}

// NewLocalSQLiteSink starts a buffered SQLite-backed telemetry sink.
func NewLocalSQLiteSink(store localStore, log *slog.Logger, retention time.Duration) *LocalSQLiteSink {
	if retention < MinRetention {
		retention = DefaultRetention
	}
	s := &LocalSQLiteSink{
		store:     store,
		log:       log,
		retention: retention,
		ch:        make(chan ports.TelemetryEvent, localBufferSize),
		now:       time.Now,
		newID:     func() string { return "tev_" + uuid.NewString() },
		sleep:     time.Sleep,
	}
	s.wg.Add(1)
	go s.loop()
	return s
}

// Emit enqueues an event for best-effort persistence.
func (s *LocalSQLiteSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	select {
	case s.ch <- ev:
	default:
		s.log.Warn("telemetry local sink buffer full; dropping event", "name", ev.Name, "source", ev.Source)
	}
}

// Close drains the worker until completion or context cancellation.
func (s *LocalSQLiteSink) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.ch) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.wg.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *LocalSQLiteSink) loop() {
	defer s.wg.Done()
	for ev := range s.ch {
		s.persist(ev)
	}
}

func (s *LocalSQLiteSink) persist(ev ports.TelemetryEvent) {
	payloadJSON, err := json.Marshal(ev.Payload)
	if err != nil {
		s.log.Warn("telemetry payload marshal failed", "name", ev.Name, "error", err)
		return
	}
	rec := sqlitestore.TelemetryEventRecord{
		ID:          s.newID(),
		OccurredAt:  ev.OccurredAt.UTC(),
		Name:        ev.Name,
		Source:      ev.Source,
		Level:       string(ev.Level),
		ProjectID:   ev.ProjectID,
		SessionID:   ev.SessionID,
		RequestID:   ev.RequestID,
		PayloadJSON: string(payloadJSON),
	}
	if err := s.store.CreateTelemetryEvent(context.Background(), rec); err != nil {
		s.log.Warn("telemetry local sink write failed", "name", ev.Name, "error", err)
		return
	}
	s.maybePrune()
}

func (s *LocalSQLiteSink) maybePrune() {
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	now := s.now().UTC()
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < localPruneEvery {
		return
	}
	s.lastPrune = now
	cutoff := now.Add(-s.retention)

	var totalDeleted int64
	for {
		n, err := s.store.PruneTelemetryEventsBefore(context.Background(), cutoff, localPruneBatchLimit)
		if err != nil {
			s.log.Warn("telemetry prune failed", "error", err)
			break
		}
		totalDeleted += n
		if n < localPruneBatchLimit {
			break
		}
		s.sleep(localPruneBatchPause)
	}

	if totalDeleted > 0 {
		s.log.Debug("telemetry pruned", "rows", totalDeleted)
	}

	s.maybeVacuum()
}

func (s *LocalSQLiteSink) maybeVacuum() {
	free, err := s.store.FreelistCount(context.Background())
	if err != nil {
		s.log.Warn("telemetry freelist check failed", "error", err)
		return
	}
	if free < localVacuumThreshold {
		return
	}
	if err := s.store.IncrementalVacuum(context.Background(), localVacuumPages); err != nil {
		s.log.Warn("telemetry vacuum failed", "error", err)
		return
	}
	s.log.Debug("telemetry vacuum reclaimed pages", "requested", localVacuumPages)
}
