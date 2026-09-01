package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// WorkerAgentSessionID returns the latest provider-native conversation identity
// captured by a TUI or Chat harness hook, fenced to the calling worker epoch.
func (s *Store) WorkerAgentSessionID(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (string, error) {
	var id string
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		return tx.QueryRow(ctx, `SELECT agent_session_id FROM ao_sessions WHERE org_id = $1 AND id = $2 AND is_terminated = false`, orgID, sessionID).Scan(&id)
	})
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return id, err
}
