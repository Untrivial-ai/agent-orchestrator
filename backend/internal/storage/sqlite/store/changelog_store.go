package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// EventsAfter implements cdc.Source over the SQLite change_log table.
func (s *Store) EventsAfter(ctx context.Context, after int64, limit int) ([]cdc.Event, error) {
	rows, err := s.qr.ReadChangeLogAfter(ctx, gen.ReadChangeLogAfterParams{Seq: after, Limit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("read change_log after %d: %w", after, err)
	}
	events := make([]cdc.Event, 0, len(rows))
	for _, r := range rows {
		events = append(events, changeLogEventFromGen(r))
	}
	return events, nil
}

// LatestSeq implements cdc.Source by returning the current change_log head.
func (s *Store) LatestSeq(ctx context.Context) (int64, error) {
	seq, err := s.qr.MaxChangeLogSeq(ctx)
	if err != nil {
		return 0, fmt.Errorf("max change_log seq: %w", err)
	}
	return seq, nil
}

// PruneChangeLogBefore removes at most limit events older than before. CDC
// consumers own their cursors, so retention is deliberately best-effort and
// bounded; a slow reader may miss events older than the replay window but live
// subscribers continue to receive new events from the broadcaster.
func (s *Store) PruneChangeLogBefore(ctx context.Context, before time.Time, limit int64) (int64, error) {
	n, err := s.qw.PruneChangeLogBefore(ctx, gen.PruneChangeLogBeforeParams{
		CreatedAt: before.UTC(),
		Limit:     limit,
	})
	if err != nil {
		return 0, fmt.Errorf("prune change_log before %s: %w", before.UTC().Format(time.RFC3339), err)
	}
	return n, nil
}

// PruneChangeLogToMaxRows removes the oldest events necessary to bring the
// change_log down to maxRows, deleting at most limit rows in one transaction.
// The row cap complements age retention for high-volume installations where a
// large burst can fill the database before the age window expires.
func (s *Store) PruneChangeLogToMaxRows(ctx context.Context, maxRows, limit int64) (int64, error) {
	n, err := s.qw.PruneChangeLogToMaxRows(ctx, gen.PruneChangeLogToMaxRowsParams{
		BatchLimit: limit,
		MaxRows:    maxRows,
	})
	if err != nil {
		return 0, fmt.Errorf("prune change_log to %d rows: %w", maxRows, err)
	}
	return n, nil
}

func changeLogEventFromGen(r gen.ChangeLog) cdc.Event {
	e := cdc.Event{
		Seq:       r.Seq,
		ProjectID: string(r.ProjectID),
		Type:      r.EventType,
		Payload:   json.RawMessage(r.Payload),
		CreatedAt: r.CreatedAt,
	}
	if r.SessionID != nil {
		e.SessionID = string(*r.SessionID)
	}
	return e
}
