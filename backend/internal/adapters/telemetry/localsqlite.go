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
	localRetention       = 30 * 24 * time.Hour
	localPruneEvery      = time.Hour
	localPruneBatchLimit = int64(1000)
)

type localStore interface {
	CreateTelemetryEvent(ctx context.Context, rec sqlitestore.TelemetryEventRecord) error
	PruneTelemetryEventsBefore(ctx context.Context, before time.Time, limit int64) (int64, error)
}

// LocalSQLiteSink persists telemetry events into the daemon's SQLite database
// behind a small buffered worker so event emission stays best-effort.
type LocalSQLiteSink struct {
	store     localStore
	log       *slog.Logger
	ch        chan ports.TelemetryEvent
	wg        sync.WaitGroup
	closeOnce sync.Once
	queueMu   sync.RWMutex
	closed    bool
	now       func() time.Time
	newID     func() string

	pruneMu   sync.Mutex
	lastPrune time.Time
}

// NewLocalSQLiteSink starts a buffered SQLite-backed telemetry sink.
func NewLocalSQLiteSink(store localStore, log *slog.Logger) *LocalSQLiteSink {
	s := &LocalSQLiteSink{
		store: store,
		log:   log,
		ch:    make(chan ports.TelemetryEvent, localBufferSize),
		now:   time.Now,
		newID: func() string { return "tev_" + uuid.NewString() },
	}
	s.wg.Add(1)
	go s.loop()
	return s
}

// Emit enqueues an event for best-effort persistence.
func (s *LocalSQLiteSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.queueMu.RLock()
	if s.closed {
		s.queueMu.RUnlock()
		return
	}
	queued := false
	select {
	case s.ch <- ev:
		queued = true
	default:
	}
	s.queueMu.RUnlock()
	if !queued {
		s.log.Warn("telemetry local sink buffer full; dropping event", "name", ev.Name, "source", ev.Source)
	}
}

// Close drains the worker until completion or context cancellation.
func (s *LocalSQLiteSink) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.queueMu.Lock()
		s.closed = true
		close(s.ch)
		s.queueMu.Unlock()
	})
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
	if _, err := s.store.PruneTelemetryEventsBefore(context.Background(), now.Add(-localRetention), localPruneBatchLimit); err != nil {
		s.log.Warn("telemetry local sink prune failed", "error", err)
	}
}
