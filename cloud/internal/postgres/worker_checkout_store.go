package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// WorkerGitHubCheckoutContext resolves a worker-owned session to the exact
// GitHub App installation that may read its project repository. This is an
// organization-scoped service lookup: no caller-provided repository or
// installation identifier participates in the decision.
func (s *Store) WorkerGitHubCheckoutContext(
	ctx context.Context,
	orgID, sessionID string,
) (domain.GitHubCheckoutContext, error) {
	authorization := domain.GitHubCheckoutContext{OrgID: orgID, SessionID: sessionID}
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT project.id, installation.github_installation_id,
				repository.github_repository_id, repository.full_name,
				repository.clone_url, repository.default_branch
			FROM ao_sessions session
			JOIN ao_projects project
			  ON project.org_id = session.org_id AND project.id = session.project_id
			JOIN ao_github_repository_grants grant_row
			  ON grant_row.org_id = project.org_id
			 AND grant_row.id = project.github_repository_grant_id
			 AND grant_row.github_repository_id = project.github_repository_id
			JOIN ao_github_installations installation
			  ON installation.org_id = grant_row.org_id
			 AND installation.id = grant_row.installation_id
			JOIN ao_github_repositories repository
			  ON repository.github_repository_id = grant_row.github_repository_id
			WHERE session.org_id = $1 AND session.id = $2
			  AND session.is_terminated = false
			  AND project.github_repository_id IS NOT NULL
			  AND project.github_repository_grant_id IS NOT NULL
			  AND project.repository_url = repository.html_url
			  AND grant_row.revoked_at IS NULL
			  AND installation.status = 'active'
			  AND installation.suspended_at IS NULL
			  AND installation.disconnected_at IS NULL
			  AND installation.deleted_at IS NULL
			  AND repository.is_archived = false
			  AND repository.is_disabled = false
			  AND btrim(repository.clone_url) <> ''`,
			orgID, sessionID,
		).Scan(
			&authorization.ProjectID,
			&authorization.GitHubInstallationID,
			&authorization.GitHubRepositoryID,
			&authorization.FullName,
			&authorization.CloneURL,
			&authorization.DefaultBranch,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("resolve worker GitHub checkout context: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.GitHubCheckoutContext{}, err
	}
	return authorization, nil
}

// WorkerRemoteGitHubCheckoutContext returns only the encrypted production
// capability bound to the worker's own session. Repository and installation
// identifiers are read from the project row and are never supplied by the
// worker.
func (s *Store) WorkerRemoteGitHubCheckoutContext(
	ctx context.Context,
	orgID, sessionID string,
) (domain.RemoteGitHubCheckoutContext, error) {
	authorization := domain.RemoteGitHubCheckoutContext{
		OrgID: orgID, SessionID: sessionID,
	}
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		err := tx.QueryRow(
			ctx,
			`SELECT project.id, project.github_installation_id,
				project.github_repository_id, project.github_authority_user_id,
				project.github_authority_environment, project.repository_url,
				project.github_capability_ciphertext,
				project.github_capability_nonce
			FROM ao_sessions session
			JOIN ao_projects project
			  ON project.org_id = session.org_id
			 AND project.id = session.project_id
			WHERE session.org_id = $1 AND session.id = $2
			  AND session.is_terminated = false
			  AND project.github_repository_grant_id IS NULL
			  AND project.github_installation_id IS NOT NULL
			  AND project.github_repository_id IS NOT NULL
			  AND project.github_capability_ciphertext IS NOT NULL
			  AND project.github_capability_nonce IS NOT NULL`,
			orgID,
			sessionID,
		).Scan(
			&authorization.ProjectID,
			&authorization.GitHubInstallationID,
			&authorization.GitHubRepositoryID,
			&authorization.UserExternalID,
			&authorization.TargetEnvironment,
			&authorization.RepositoryURL,
			&authorization.CapabilityCiphertext,
			&authorization.CapabilityNonce,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("resolve remote GitHub checkout context: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.RemoteGitHubCheckoutContext{}, err
	}
	return authorization, nil
}
