package store

import (
	"context"
	"encoding/json"
	"fmt"

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

func changeLogEventFromGen(r gen.ChangeLog) cdc.Event {
	e := cdc.Event{
		Seq:       r.Seq,
		ProjectID: string(projectIDValue(r.ProjectID)),
		Type:      r.EventType,
		Payload:   json.RawMessage(r.Payload),
		CreatedAt: r.CreatedAt,
	}
	if r.SessionID != nil {
		e.SessionID = string(*r.SessionID)
	}
	return e
}
