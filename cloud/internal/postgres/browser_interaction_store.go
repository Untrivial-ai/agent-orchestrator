package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// RefreshBrowserInteraction keeps authorized interactive browsing out of idle pause.
func (s *Store) RefreshBrowserInteraction(ctx context.Context, principal domain.Principal, orgID, sessionID string, epoch int64) error {
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" {
			return ErrForbidden
		}
		var mode, desired string
		var terminated bool
		err := tx.QueryRow(ctx, `SELECT session.mode, session.is_terminated, sandbox.desired_state
			FROM ao_sessions session JOIN ao_sandboxes sandbox
			ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
			WHERE session.org_id = $1 AND session.id = $2
			FOR UPDATE OF session, sandbox`, orgID, sessionID).Scan(&mode, &terminated, &desired)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWorkerUnavailable
		}
		if err != nil {
			return err
		}
		if terminated || desired != "running" {
			return ErrWorkerUnavailable
		}
		if effectiveMode(mode, access.ModeCap) == "read-only" {
			return ErrForbidden
		}
		current, err := workerEpochCurrent(ctx, tx, orgID, sessionID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		_, err = tx.Exec(ctx, `UPDATE ao_sandboxes
			SET interactive_until = now() + $3::interval, updated_at = now()
			WHERE org_id = $1 AND session_id = $2
			AND (interactive_until IS NULL OR interactive_until <= now() + $4::interval)`,
			orgID, sessionID, intervalString(interactiveSessionLease),
			intervalString(interactiveSessionLease-interactionRefreshThrottle))
		return err
	})
	if err != nil {
		return fmt.Errorf("refresh browser interaction: %w", err)
	}
	return nil
}
