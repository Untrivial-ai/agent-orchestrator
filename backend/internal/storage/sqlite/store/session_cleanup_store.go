package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertSessionCleanupFacts records or updates the durable teardown facts for a
// terminated session. The session_cleanup_facts_cdc_* triggers fan out a
// session_updated CDC event when the disposition or runtime-release state
// actually changes (retry-bookkeeping-only writes are suppressed).
func (s *Store) UpsertSessionCleanupFacts(ctx context.Context, rec domain.SessionCleanupRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	disposition := rec.WorkspaceDisposition
	if disposition == "" {
		// The column default is 'pending' and the CHECK rejects ''; default a
		// zero-value disposition so an unset record still writes a valid row.
		disposition = domain.DispositionPending
	}
	return s.qw.UpsertSessionCleanupFacts(ctx, gen.UpsertSessionCleanupFactsParams{
		SessionID:            rec.SessionID,
		SessionGeneration:    rec.SessionGeneration,
		RuntimeReleasedAt:    timeToNullTime(rec.RuntimeReleasedAt),
		WorkspaceDisposition: string(disposition),
		AttemptCount:         rec.AttemptCount,
		LastAttemptAt:        timeToNullTime(rec.LastAttemptAt),
		NextAttemptAt:        timeToNullTime(rec.NextAttemptAt),
		FailureCode:          rec.FailureCode,
	})
}

// GetSessionCleanupFacts returns the cleanup facts for a session, or ok=false
// when the session has no facts row yet.
func (s *Store) GetSessionCleanupFacts(ctx context.Context, id domain.SessionID) (domain.SessionCleanupRecord, bool, error) {
	row, err := s.qr.GetSessionCleanupFacts(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionCleanupRecord{}, false, nil
	}
	if err != nil {
		return domain.SessionCleanupRecord{}, false, fmt.Errorf("get session cleanup facts %s: %w", id, err)
	}
	return sessionCleanupFromGen(row), true, nil
}

// listTerminalCleanupCandidatesSQL is the coarse, SESSIONS-driven candidate scan
// for the terminal-resource reconciler's boot / periodic pass. It is a
// `sessions LEFT JOIN facts` (NOT a facts-only `WHERE disposition='pending'`) so
// the pre-existing leaked backlog — terminated sessions with NO facts row — is
// still surfaced. A candidate is a terminated session with no facts row yet,
// facts stale relative to the session's cleanup_generation, a new pending
// cleanup, or a scheduled workspace/runtime retry due at or before now. A
// terminal workspace disposition is revisited automatically only while an
// unresolved runtime release has a scheduled retry. Once the retry budget is
// exhausted next_attempt_at is cleared, so only a user-triggered cleanup can
// revisit it. The restorable-marker exclusion and terminal re-check are applied
// by the Go finalizer under the per-session lock, not in SQL.
//
// It runs as raw SQL rather than a sqlc query because sqlc 1.31's SQLite parser
// truncates the trailing clause after the final `?` placeholder — the scan's
// parameter is nested inside a WHERE group whose closing parens would be stripped
// (see the note in queries/cleanup.sql).
const listTerminalCleanupCandidatesSQL = `
SELECT s.id
FROM sessions s
LEFT JOIN session_cleanup_facts f ON f.session_id = s.id
WHERE s.is_terminated = 1
  AND (
      f.session_id IS NULL
      OR f.session_generation < s.cleanup_generation
      OR (
          f.workspace_disposition = 'pending'
          AND f.next_attempt_at IS NULL
      )
      OR (
          (f.workspace_disposition = 'pending' OR f.runtime_released_at IS NULL)
          AND f.next_attempt_at <= ?
      )
  )
ORDER BY s.project_id, s.num;`

// ListTerminalCleanupCandidates returns the ids of terminated sessions whose
// runtime/workspace resources may still need releasing (see
// listTerminalCleanupCandidatesSQL).
func (s *Store) ListTerminalCleanupCandidates(ctx context.Context, now time.Time) ([]domain.SessionID, error) {
	rows, err := s.readDB.QueryContext(ctx, listTerminalCleanupCandidatesSQL, timeToNullTime(now))
	if err != nil {
		return nil, fmt.Errorf("list terminal cleanup candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.SessionID
	for rows.Next() {
		var id domain.SessionID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan terminal cleanup candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list terminal cleanup candidates: %w", err)
	}
	return ids, nil
}

func sessionCleanupFromGen(row gen.SessionCleanupFact) domain.SessionCleanupRecord {
	return domain.SessionCleanupRecord{
		SessionID:            row.SessionID,
		SessionGeneration:    row.SessionGeneration,
		RuntimeReleasedAt:    nullTimeToTime(row.RuntimeReleasedAt),
		WorkspaceDisposition: domain.WorkspaceDisposition(row.WorkspaceDisposition),
		AttemptCount:         row.AttemptCount,
		LastAttemptAt:        nullTimeToTime(row.LastAttemptAt),
		NextAttemptAt:        nullTimeToTime(row.NextAttemptAt),
		FailureCode:          row.FailureCode,
	}
}
