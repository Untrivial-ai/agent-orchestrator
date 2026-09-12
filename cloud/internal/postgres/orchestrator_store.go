package postgres

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ListOrchestratorChildren(
	ctx context.Context,
	orgID, orchestratorSessionID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.Session, bool, error) {
	var sessions []domain.Session
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := requireActiveOrchestrator(ctx, tx, orgID, orchestratorSessionID); err != nil {
			return err
		}
		rows, err := tx.Query(
			ctx,
			sessionSelect+`
			WHERE session.org_id = $1
			  AND session.parent_session_id = $2
			  AND ($3::timestamptz IS NULL OR (session.updated_at, session.id) < ($3, $4::uuid))
			ORDER BY session.updated_at DESC, session.id DESC
			LIMIT $5`,
			orgID, orchestratorSessionID, cursorTime(cursor), cursorID(cursor), limit+1,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var session domain.Session
			if err := scanSession(rows, &session); err != nil {
				return err
			}
			sessions = append(sessions, session)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}
	return sessions, hasMore, nil
}

func (s *Store) CreateOrchestratorChild(
	ctx context.Context,
	orgID, orchestratorSessionID, idempotencyKey string,
	maxActiveSandboxes int,
	input domain.CreateSession,
) (domain.Session, error) {
	var child domain.Session
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var projectID string
		var createdByUserID *string
		err := tx.QueryRow(
			ctx,
			`SELECT project_id, created_by_user_id::text
			FROM ao_sessions
			WHERE org_id = $1 AND id = $2
			  AND kind = 'orchestrator' AND is_terminated = false`,
			orgID, orchestratorSessionID,
		).Scan(&projectID, &createdByUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		input.ProjectID = projectID
		input.Kind = "worker"
		creator := ""
		if createdByUserID != nil {
			creator = *createdByUserID
		}
		child, err = createSessionTx(
			ctx, tx, orgID, idempotencyKey, maxActiveSandboxes,
			input, orchestratorSessionID, creator,
		)
		return err
	})
	return child, err
}

func (s *Store) SendOrchestratorChildMessage(
	ctx context.Context,
	orgID, orchestratorSessionID, childSessionID, idempotencyKey, text string,
) (domain.ClientEvent, error) {
	var event domain.ClientEvent
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		projectID, err := requireActiveOrchestrator(ctx, tx, orgID, orchestratorSessionID)
		if err != nil {
			return err
		}
		var allowed bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1 FROM ao_sessions
				WHERE org_id = $1
				  AND id = $2
				  AND project_id = $3
				  AND parent_session_id = $4
			)`,
			orgID, childSessionID, projectID, orchestratorSessionID,
		).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		event, err = sendMessageTx(
			ctx, tx, orgID, childSessionID, idempotencyKey, text, "", orchestratorSessionID,
			"", nil,
		)
		return err
	})
	return event, err
}

func (s *Store) DeleteOrchestratorChild(
	ctx context.Context,
	orgID, orchestratorSessionID, childSessionID string,
) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		projectID, err := requireActiveOrchestrator(
			ctx, tx, orgID, orchestratorSessionID,
		)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE ao_sandboxes AS sandbox
			SET desired_state = $1, startup_started_at = NULL,
				reconcile_after = now(), updated_at = now()
			FROM ao_sessions AS session
			WHERE sandbox.org_id = $2
			  AND sandbox.session_id = $3
			  AND session.org_id = sandbox.org_id
			  AND session.id = sandbox.session_id
			  AND session.project_id = $4
			  AND session.parent_session_id = $5`,
			domain.SandboxDesiredDeleted, orgID, childSessionID,
			projectID, orchestratorSessionID,
		)
		if err != nil {
			return normalizeConstraintError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrForbidden
		}
		return nil
	})
}

func requireActiveOrchestrator(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID string,
) (string, error) {
	var projectID string
	err := tx.QueryRow(
		ctx,
		`SELECT project_id
		FROM ao_sessions
		WHERE org_id = $1 AND id = $2
		  AND kind = 'orchestrator' AND is_terminated = false`,
		orgID, sessionID,
	).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return projectID, err
}
