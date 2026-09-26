package postgres

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) BeginSandboxCreation(ctx context.Context, owner string, record domain.Sandbox, id string) error {
	return s.withOrg(ctx, record.OrgID, func(tx pgx.Tx) error {
		var current bool
		if err := tx.QueryRow(ctx, `SELECT reconcile_lease_owner = $3 AND reconcile_lease_until > now()
			AND preparation_generation = $4 AND desired_state <> 'deleted'
			FROM ao_sandboxes WHERE org_id = $1 AND session_id = $2 FOR UPDATE`,
			record.OrgID, record.SessionID, owner, record.PreparationGeneration).Scan(&current); err != nil {
			return err
		}
		if !current {
			return ErrSandboxLeaseLost
		}
		_, err := tx.Exec(ctx, `INSERT INTO ao_sandbox_creations (id, org_id, session_id, generation)
			VALUES ($1, $2, $3, $4)`, id, record.OrgID, record.SessionID, record.PreparationGeneration)
		return err
	})
}

func (s *Store) RecordSandboxCreationResult(ctx context.Context, orgID, sessionID, id, environmentID string) error {
	if environmentID == "" {
		return ErrInvalid
	}
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		// Match deletion's lock order so absence cannot commit over a late result.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ao_sandboxes WHERE org_id = $1 AND session_id = $2 FOR UPDATE`, orgID, sessionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE ao_sandbox_creations
			SET provider_environment_id = $4,
			    state = CASE WHEN provider_environment_id = $4 AND state IN ('adopted', 'deleted') THEN state ELSE 'created' END,
			    updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3`, orgID, sessionID, id, environmentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `UPDATE ao_sandboxes SET reconcile_after = now(),
			observed_state = CASE WHEN observed_state = 'deleted' THEN 'deleting' ELSE observed_state END
			WHERE org_id = $1 AND session_id = $2`, orgID, sessionID)
		return err
	})
}

func (s *Store) ListSandboxCreations(ctx context.Context, orgID, sessionID string) ([]domain.SandboxCreation, error) {
	var creations []domain.SandboxCreation
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, generation, provider_environment_id FROM ao_sandbox_creations
			WHERE org_id = $1 AND session_id = $2 AND state IN ('creating', 'created') ORDER BY created_at`, orgID, sessionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var creation domain.SandboxCreation
			if err := rows.Scan(&creation.ID, &creation.Generation, &creation.EnvironmentID); err != nil {
				return err
			}
			creations = append(creations, creation)
		}
		return rows.Err()
	})
	return creations, err
}

func (s *Store) ResolveSandboxCreation(ctx context.Context, orgID, sessionID, id, state string) error {
	if state != "adopted" && state != "deleted" {
		return ErrInvalid
	}
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ao_sandbox_creations SET state = $4, updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			AND ($4 = 'deleted' OR EXISTS (SELECT 1 FROM ao_sandboxes sandbox
				WHERE sandbox.org_id = $1 AND sandbox.session_id = $2 AND sandbox.desired_state <> 'deleted'
				AND sandbox.provider_environment_id = ao_sandbox_creations.provider_environment_id))`, orgID, sessionID, id, state)
		if err != nil {
			return fmt.Errorf("resolve sandbox creation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}
