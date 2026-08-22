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

// ChangeLogRetentionRows caps how many recent change_log events are retained.
// The log is an invalidation feed: clients that fall further behind than this
// refetch state on reconnect instead of replaying history (see httpd.events).
const ChangeLogRetentionRows = 100_000

// PruneChangeLog deletes change_log rows beyond the newest keep events so the
// append-only CDC log cannot grow unbounded (#3963). The delete runs by the seq
// PK directly rather than through sqlc: sqlc 1.31's SQLite parser mangles
// DELETE placeholders (see queries/changelog.sql), and nothing references
// change_log, so there is no FK fallout. A truncating WAL checkpoint follows a
// successful prune (best effort) so freed pages return to the OS promptly.
func (s *Store) PruneChangeLog(ctx context.Context, keep int64) (int64, error) {
	if keep <= 0 {
		keep = ChangeLogRetentionRows
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	maxSeq, err := s.qw.MaxChangeLogSeq(ctx)
	if err != nil {
		return 0, fmt.Errorf("max change_log seq: %w", err)
	}
	floor := maxSeq - keep
	if floor <= 0 {
		return 0, nil
	}
	res, err := s.writeDB.ExecContext(ctx, `DELETE FROM change_log WHERE seq <= ?`, floor)
	if err != nil {
		return 0, fmt.Errorf("prune change_log through %d: %w", floor, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune change_log through %d: rows affected: %w", floor, err)
	}
	if n > 0 {
		_, _ = s.writeDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
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
