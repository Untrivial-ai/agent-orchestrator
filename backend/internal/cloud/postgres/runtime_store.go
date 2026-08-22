package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

// CreateSessionRuntime records a new provisioning attempt for one AO session.
func (s *Store) CreateSessionRuntime(ctx context.Context, principal domain.Principal, workspace domain.Workspace, sessionID string) (domain.SessionRuntime, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SessionRuntime{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`, principal.UserID, workspace.OrgID); err != nil {
		return domain.SessionRuntime{}, err
	}
	var runtime domain.SessionRuntime
	err = tx.QueryRow(ctx, `INSERT INTO ao_cloud_session_runtimes (workspace_id, org_id, session_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, session_id) DO UPDATE SET state = 'provisioning', sandbox_id = '', error = '', generation = ao_cloud_session_runtimes.generation + 1, updated_at = now()
		RETURNING id, workspace_id, org_id, session_id, sandbox_id, state, error, generation, created_at, updated_at`,
		workspace.ID, workspace.OrgID, strings.TrimSpace(sessionID)).Scan(
		&runtime.ID, &runtime.WorkspaceID, &runtime.OrgID, &runtime.SessionID, &runtime.SandboxID,
		&runtime.State, &runtime.Error, &runtime.Generation, &runtime.CreatedAt, &runtime.UpdatedAt)
	if err != nil {
		return domain.SessionRuntime{}, normalizeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SessionRuntime{}, err
	}
	return runtime, nil
}

// SessionRuntime returns one tenant-scoped runtime mapping.
func (s *Store) SessionRuntime(ctx context.Context, principal domain.Principal, orgID, workspaceID, sessionID string) (domain.SessionRuntime, error) {
	workspace, err := s.Workspace(ctx, principal, orgID, workspaceID)
	if err != nil {
		return domain.SessionRuntime{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.SessionRuntime{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`, principal.UserID, workspace.OrgID); err != nil {
		return domain.SessionRuntime{}, err
	}
	runtime, err := scanSessionRuntime(tx.QueryRow(ctx, `SELECT id, workspace_id, org_id, session_id, sandbox_id, state, error, generation, created_at, updated_at FROM ao_cloud_session_runtimes WHERE workspace_id = $1 AND session_id = $2`, workspaceID, sessionID))
	if err != nil {
		return domain.SessionRuntime{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SessionRuntime{}, err
	}
	return runtime, nil
}

// RuntimeWorkspace resolves the tenant-visible project named by a verified
// workspace capability.
func (s *Store) RuntimeWorkspace(ctx context.Context, principal domain.Principal, orgID, workspaceID string) (domain.Workspace, error) {
	return s.Workspace(ctx, principal, orgID, workspaceID)
}

// UpdateSessionRuntime records provider state for one runtime mapping.
func (s *Store) UpdateSessionRuntime(ctx context.Context, principal domain.Principal, runtime domain.SessionRuntime, state, sandboxID, failure string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`, principal.UserID, runtime.OrgID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE ao_cloud_session_runtimes SET state=$2, sandbox_id=$3, error=$4, updated_at=now() WHERE id=$1 AND generation=$5`, runtime.ID, state, strings.TrimSpace(sandboxID), strings.TrimSpace(failure), runtime.Generation)
	if err != nil {
		return normalizeError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func scanSessionRuntime(row rowScanner) (domain.SessionRuntime, error) {
	var runtime domain.SessionRuntime
	if err := row.Scan(&runtime.ID, &runtime.WorkspaceID, &runtime.OrgID, &runtime.SessionID, &runtime.SandboxID, &runtime.State, &runtime.Error, &runtime.Generation, &runtime.CreatedAt, &runtime.UpdatedAt); err != nil {
		return domain.SessionRuntime{}, normalizeError(err)
	}
	return runtime, nil
}
