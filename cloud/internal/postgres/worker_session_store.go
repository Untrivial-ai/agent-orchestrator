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

// WorkerSessionModel returns the model most recently selected by ChatUI,
// fenced to the calling worker epoch. TUI command rebuilds use this rather
// than the model captured during the worker's initial bootstrap.
func (s *Store) WorkerSessionModel(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (string, error) {
	var model string
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		return tx.QueryRow(ctx, `SELECT model FROM ao_sessions WHERE org_id = $1 AND id = $2 AND is_terminated = false`, orgID, sessionID).Scan(&model)
	})
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return model, err
}

func (s *Store) WorkerSessionReasoningEffort(ctx context.Context, orgID, sessionID, workerID string, epoch int64) (string, error) {
	var effort string
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		return tx.QueryRow(ctx, `SELECT reasoning_effort FROM ao_sessions WHERE org_id = $1 AND id = $2 AND is_terminated = false`, orgID, sessionID).Scan(&effort)
	})
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return effort, err
}
