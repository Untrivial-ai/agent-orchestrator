package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

// CreateWorkspace records tenant-scoped provisioning intent.
func (s *Store) CreateWorkspace(
	ctx context.Context,
	principal domain.Principal,
	orgID, repositoryURL, repositoryRef string,
) (domain.Workspace, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		principal.UserID, strings.TrimSpace(orgID)); err != nil {
		return domain.Workspace{}, err
	}
	var activeMember bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM ao_org_memberships
			WHERE org_id = $1 AND user_id = $2 AND status = 'active'
		)`, strings.TrimSpace(orgID), principal.UserID).Scan(&activeMember); err != nil {
		return domain.Workspace{}, err
	}
	if !activeMember {
		return domain.Workspace{}, ErrInvalid
	}
	var workspace domain.Workspace
	err = tx.QueryRow(ctx,
		`INSERT INTO ao_cloud_workspaces (
			org_id, owner_user_id, repository_url, repository_ref
		) VALUES ($1, $2, $3, $4)
		RETURNING id, org_id, owner_user_id, repository_url, repository_ref,
		          sandbox_id, state, error, created_at, updated_at`,
		strings.TrimSpace(orgID), principal.UserID, strings.TrimSpace(repositoryURL), strings.TrimSpace(repositoryRef),
	).Scan(&workspace.ID, &workspace.OrgID, &workspace.OwnerUserID,
		&workspace.RepositoryURL, &workspace.RepositoryRef, &workspace.SandboxID,
		&workspace.State, &workspace.Error, &workspace.CreatedAt, &workspace.UpdatedAt)
	if err != nil {
		return domain.Workspace{}, normalizeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Workspace{}, err
	}
	return workspace, nil
}

// Workspace returns one tenant-visible cloud workspace.
func (s *Store) Workspace(
	ctx context.Context, principal domain.Principal, orgID, workspaceID string,
) (domain.Workspace, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		principal.UserID, strings.TrimSpace(orgID)); err != nil {
		return domain.Workspace{}, err
	}
	workspace, err := scanWorkspace(tx.QueryRow(ctx,
		`SELECT id, org_id, owner_user_id, repository_url, repository_ref,
		        sandbox_id, state, error, created_at, updated_at
		 FROM ao_cloud_workspaces WHERE id = $1 AND org_id = $2`,
		strings.TrimSpace(workspaceID), strings.TrimSpace(orgID)))
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Workspace{}, err
	}
	return workspace, nil
}

// UpdateWorkspaceProvisioning records provider state for the workspace owner.
func (s *Store) UpdateWorkspaceProvisioning(
	ctx context.Context, workspace domain.Workspace, state, sandboxID, failure string,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		workspace.OwnerUserID, workspace.OrgID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx,
		`UPDATE ao_cloud_workspaces
		 SET state = $2, sandbox_id = $3, error = $4, updated_at = now()
		 WHERE id = $1 AND org_id = $5 AND owner_user_id = $6`,
		workspace.ID, state, strings.TrimSpace(sandboxID), strings.TrimSpace(failure), workspace.OrgID, workspace.OwnerUserID)
	if err != nil {
		return normalizeError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

type rowScanner interface {
	Scan(...any) error
}

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var workspace domain.Workspace
	if err := row.Scan(&workspace.ID, &workspace.OrgID, &workspace.OwnerUserID,
		&workspace.RepositoryURL, &workspace.RepositoryRef, &workspace.SandboxID,
		&workspace.State, &workspace.Error, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
		return domain.Workspace{}, normalizeError(err)
	}
	return workspace, nil
}
