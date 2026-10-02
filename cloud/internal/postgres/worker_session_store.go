package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// WorkerAgentSessionID reads the provider-native conversation identity for the
// current worker epoch so a reopened TUI resumes the Chat conversation.
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
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
