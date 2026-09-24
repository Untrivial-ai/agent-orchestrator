package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ListActiveSessionRecords avoids reading the archive on active-only board
// requests. Existing list endpoints retain their original behavior.
func (s *Store) ListActiveSessionRecords(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT id FROM sessions WHERE is_terminated = 0 AND (? = '' OR project_id = ?) ORDER BY project_id, num`, project, project)
	if err != nil {
		return nil, fmt.Errorf("list active session ids: %w", err)
	}
	ids := make([]domain.SessionID, 0)
	for rows.Next() {
		var id domain.SessionID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan active session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read active session ids: %w", err)
	}
	_ = rows.Close()
	recs := make([]domain.SessionRecord, 0, len(ids))
	for _, id := range ids {
		rec, exists, err := s.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		if exists && !rec.IsTerminated {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

// ListSessionHistory returns at most Limit+1 identities. The extra row allows
// the service to produce a next cursor without an unbounded count query.
func (s *Store) ListSessionHistory(ctx context.Context, filter domain.SessionHistoryFilter) ([]domain.SessionHistoryEntry, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return nil, fmt.Errorf("history limit must be between 1 and 100")
	}
	// The wildcard characters are literal user text, not query syntax.
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	query = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	pattern := "%" + query + "%"
	rows, err := s.readDB.QueryContext(ctx, `
SELECT id, stopped_at, history_sort_epoch
FROM sessions
WHERE is_terminated = 1
  AND (? = '' OR project_id = ?)
  AND (? = '' OR kind = ?)
  AND (? = ''
    OR (? = 'no_pr' AND NOT EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id))
    OR (? = 'merged' AND EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id)
      AND NOT EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id AND pr.pr_state <> 'merged'))
    OR (? = 'open' AND EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id AND pr.pr_state IN ('open', 'draft')))
    OR (? = 'closed_unmerged' AND EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id AND pr.pr_state = 'closed')
      AND NOT EXISTS (SELECT 1 FROM pr WHERE pr.session_id = sessions.id AND pr.pr_state IN ('open', 'draft', 'merged'))))
  AND (? = '' OR lower(display_name) LIKE ? ESCAPE '\' OR lower(id) LIKE ? ESCAPE '\' OR lower(branch) LIKE ? ESCAPE '\')
  AND (? = 0 OR (stopped_at IS NOT NULL AND history_sort_epoch >= ?))
  AND (? = 0 OR history_sort_epoch < ? OR (history_sort_epoch = ? AND id < ?))
ORDER BY history_sort_epoch DESC, id DESC
LIMIT ?`,
		filter.ProjectID, filter.ProjectID,
		filter.Kind, filter.Kind,
		filter.Delivery, filter.Delivery, filter.Delivery, filter.Delivery, filter.Delivery,
		query, pattern, pattern, pattern,
		filter.SinceEpoch, filter.SinceEpoch,
		filter.BeforeEpoch, filter.BeforeEpoch, filter.BeforeEpoch, filter.BeforeID,
		filter.Limit+1)
	if err != nil {
		return nil, fmt.Errorf("list session history: %w", err)
	}
	defer rows.Close()
	entries := make([]domain.SessionHistoryEntry, 0, filter.Limit+1)
	for rows.Next() {
		var entry domain.SessionHistoryEntry
		var stopped sql.NullTime
		if err := rows.Scan(&entry.ID, &stopped, &entry.SortEpoch); err != nil {
			return nil, fmt.Errorf("scan session history: %w", err)
		}
		if stopped.Valid {
			entry.StoppedAt = &stopped.Time
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read session history: %w", err)
	}
	return entries, nil
}
