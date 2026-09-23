package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ReserveGitHubRepositoryCapability(
	ctx context.Context,
	principal domain.Principal,
	orgID, targetEnvironment, idempotencyKey string,
	requestHash []byte,
	githubInstallationID int64,
) (domain.GitHubRepositoryCapability, bool, error) {
	var capability domain.GitHubRepositoryCapability
	created := false
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		err := tx.QueryRow(
			ctx,
			`INSERT INTO ao_github_repository_capabilities (
				org_id, user_id, github_user_id, target_environment,
				idempotency_key, request_hash, github_installation_id
			)
			SELECT $1, $2, connection.github_user_id, $3, $4, $5, $6
			FROM ao_github_user_connections connection
			WHERE connection.user_id = $2
			ON CONFLICT (org_id, user_id, target_environment, idempotency_key)
			DO NOTHING
			RETURNING id, org_id, user_id, github_user_id, target_environment,
				idempotency_key, request_hash, status, github_installation_id,
				COALESCE(github_repository_id, 0),
				COALESCE(capability_hash, '\x'::bytea),
				COALESCE(capability_ciphertext, '\x'::bytea),
				COALESCE(capability_nonce, '\x'::bytea),
				repository_owner, repository_name, created_at, updated_at`,
			orgID,
			principal.UserID,
			targetEnvironment,
			idempotencyKey,
			requestHash,
			githubInstallationID,
		).Scan(capabilityScanTargets(&capability)...)
		if err == nil {
			created = true
			capability.UserExternalID = principal.ExternalID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(
			ctx,
			`SELECT capability.id, capability.org_id, capability.user_id,
				capability.github_user_id, capability.target_environment,
				capability.idempotency_key, capability.request_hash,
				capability.status, capability.github_installation_id,
				COALESCE(capability.github_repository_id, 0),
				COALESCE(capability.capability_hash, '\x'::bytea),
				COALESCE(capability.capability_ciphertext, '\x'::bytea),
				COALESCE(capability.capability_nonce, '\x'::bytea),
				capability.repository_owner, capability.repository_name,
				capability.created_at, capability.updated_at
			FROM ao_github_repository_capabilities capability
			WHERE capability.org_id = $1
			  AND capability.user_id = $2
			  AND capability.target_environment = $3
			  AND capability.idempotency_key = $4`,
			orgID,
			principal.UserID,
			targetEnvironment,
			idempotencyKey,
		).Scan(capabilityScanTargets(&capability)...)
		if err != nil {
			return err
		}
		if !bytes.Equal(capability.RequestHash, requestHash) ||
			capability.GitHubInstallationID != githubInstallationID {
			return ErrIdempotencyMismatch
		}
		if capability.Status == "revoked" {
			return ErrConflict
		}
		capability.UserExternalID = principal.ExternalID
		if capability.GitHubRepositoryID > 0 {
			return loadCapabilityRepository(ctx, tx, &capability)
		}
		return nil
	})
	return capability, created, err
}

func (s *Store) ActivateGitHubRepositoryCapability(
	ctx context.Context,
	principal domain.Principal,
	orgID, capabilityID string,
	repository domain.GitHubRepository,
	capabilityHash, ciphertext, nonce []byte,
) (domain.GitHubRepositoryCapability, error) {
	var capability domain.GitHubRepositoryCapability
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var storedOrgID string
		if err := tx.QueryRow(
			ctx,
			`SELECT org_id FROM ao_github_repository_capabilities
			WHERE org_id = $1 AND id = $2 AND user_id = $3
			  AND status IN ('creating', 'active')
			FOR UPDATE`,
			orgID,
			capabilityID,
			principal.UserID,
		).Scan(&storedOrgID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_repositories (
				github_repository_id, github_owner_account_id, name, full_name,
				html_url, clone_url, ssh_url, default_branch, visibility,
				is_private, is_archived, is_disabled, github_updated_at,
				last_synced_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now())
			ON CONFLICT (github_repository_id) DO UPDATE SET
				github_owner_account_id = EXCLUDED.github_owner_account_id,
				name = EXCLUDED.name, full_name = EXCLUDED.full_name,
				html_url = EXCLUDED.html_url, clone_url = EXCLUDED.clone_url,
				ssh_url = EXCLUDED.ssh_url,
				default_branch = EXCLUDED.default_branch,
				visibility = EXCLUDED.visibility,
				is_private = EXCLUDED.is_private,
				is_archived = EXCLUDED.is_archived,
				is_disabled = EXCLUDED.is_disabled,
				github_updated_at = EXCLUDED.github_updated_at,
				last_synced_at = now()`,
			repository.GitHubRepositoryID,
			repository.GitHubOwnerID,
			repository.Name,
			repository.FullName,
			repository.HTMLURL,
			repository.CloneURL,
			repository.SSHURL,
			repository.DefaultBranch,
			repository.Visibility,
			repository.IsPrivate,
			repository.IsArchived,
			repository.IsDisabled,
			repository.GitHubUpdatedAt,
		); err != nil {
			return err
		}
		err := tx.QueryRow(
			ctx,
			`UPDATE ao_github_repository_capabilities
			SET status = 'active', github_repository_id = $3,
				capability_hash = $4, capability_ciphertext = $5,
				capability_nonce = $6, repository_owner = $7,
				repository_name = $8, updated_at = now()
			WHERE id = $1 AND user_id = $2
			  AND (
				status = 'creating'
				OR (
					status = 'active'
					AND github_repository_id = $3
					AND capability_hash = $4
				)
			  )
			RETURNING id, org_id, user_id, github_user_id, target_environment,
				idempotency_key, request_hash, status, github_installation_id,
				github_repository_id, capability_hash, capability_ciphertext,
				capability_nonce, repository_owner, repository_name,
				created_at, updated_at`,
			capabilityID,
			principal.UserID,
			repository.GitHubRepositoryID,
			capabilityHash,
			ciphertext,
			nonce,
			repositoryOwner(repository.FullName),
			repository.Name,
		).Scan(capabilityScanTargets(&capability)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_repository_capability_routes (
				capability_hash, capability_id, org_id, user_id,
				target_environment
			) VALUES ($1,$2,$3,$4,$5)`,
			capabilityHash,
			capability.ID,
			capability.OrgID,
			capability.UserID,
			capability.TargetEnvironment,
		); err != nil {
			return err
		}
		capability.UserExternalID = principal.ExternalID
		capability.Repository = repository
		return nil
	})
	return capability, err
}

func (s *Store) GitHubRepositoryCapability(
	ctx context.Context,
	capabilityHash []byte,
	targetEnvironment string,
) (domain.GitHubRepositoryCapability, error) {
	var capabilityID, orgID, userID, routedEnvironment string
	err := s.pool.QueryRow(
		ctx,
		`SELECT capability_id, org_id, user_id, target_environment
		FROM ao_github_repository_capability_routes
		WHERE capability_hash = $1`,
		capabilityHash,
	).Scan(&capabilityID, &orgID, &userID, &routedEnvironment)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubRepositoryCapability{}, ErrNotFound
	}
	if err != nil {
		return domain.GitHubRepositoryCapability{}, err
	}
	if routedEnvironment != targetEnvironment {
		return domain.GitHubRepositoryCapability{}, ErrForbidden
	}
	var capability domain.GitHubRepositoryCapability
	err = s.withGitHubOrg(ctx, orgID, userID, func(tx pgx.Tx) error {
		err := tx.QueryRow(
			ctx,
			`SELECT capability.id, capability.org_id, capability.user_id,
				capability.github_user_id, capability.target_environment,
				capability.idempotency_key, capability.request_hash,
				capability.status, capability.github_installation_id,
				capability.github_repository_id, capability.capability_hash,
				capability.capability_ciphertext, capability.capability_nonce,
				capability.repository_owner, capability.repository_name,
				capability.created_at, capability.updated_at,
				ao_user.external_user_id
			FROM ao_github_repository_capabilities capability
			JOIN ao_users ao_user ON ao_user.id = capability.user_id
			JOIN ao_github_repository_grants grant_row
			  ON grant_row.org_id = capability.org_id
			 AND grant_row.github_repository_id = capability.github_repository_id
			 AND grant_row.revoked_at IS NULL
			JOIN ao_github_installations installation
			  ON installation.org_id = grant_row.org_id
			 AND installation.id = grant_row.installation_id
			 AND installation.github_installation_id = capability.github_installation_id
			 AND installation.status = 'active'
			WHERE capability.id = $1
			  AND capability.status = 'active'
			  AND capability.capability_hash = $2
			  AND capability.target_environment = $3`,
			capabilityID,
			capabilityHash,
			targetEnvironment,
		).Scan(append(capabilityScanTargets(&capability), &capability.UserExternalID)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		return loadCapabilityRepository(ctx, tx, &capability)
	})
	return capability, err
}

func (s *Store) RevokeGitHubRepositoryCapability(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	capabilityHash []byte,
	reason string,
) (domain.GitHubRepositoryCapability, error) {
	var capability domain.GitHubRepositoryCapability
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		err := tx.QueryRow(
			ctx,
			`UPDATE ao_github_repository_capabilities
			SET status = 'revoked', revoke_reason = $4,
				revoked_at = COALESCE(revoked_at, now()),
				capability_ciphertext = NULL, capability_nonce = NULL,
				updated_at = now()
			WHERE org_id = $1 AND user_id = $2 AND capability_hash = $3
			RETURNING id, org_id, user_id, github_user_id, target_environment,
				idempotency_key, request_hash, status, github_installation_id,
				COALESCE(github_repository_id, 0), capability_hash,
				'\x'::bytea, '\x'::bytea, repository_owner, repository_name,
				created_at, updated_at`,
			orgID,
			principal.UserID,
			capabilityHash,
			reason,
		).Scan(capabilityScanTargets(&capability)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return capability, err
}

func (s *Store) RevokeGitHubRepositoryCapabilitiesForUser(
	ctx context.Context,
	userID, reason string,
) error {
	rows, err := s.pool.Query(
		ctx,
		`SELECT DISTINCT org_id
		FROM ao_github_repository_capability_routes
		WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return err
	}
	var orgIDs []string
	for rows.Next() {
		var orgID string
		if err := rows.Scan(&orgID); err != nil {
			rows.Close()
			return err
		}
		orgIDs = append(orgIDs, orgID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, orgID := range orgIDs {
		if err := s.withGitHubOrg(ctx, orgID, userID, func(tx pgx.Tx) error {
			_, err := tx.Exec(
				ctx,
				`UPDATE ao_github_repository_capabilities
				SET status = 'revoked', revoke_reason = $3,
					revoked_at = COALESCE(revoked_at, now()),
					capability_ciphertext = NULL,
					capability_nonce = NULL, updated_at = now()
				WHERE org_id = $1 AND user_id = $2 AND status <> 'revoked'`,
				orgID,
				userID,
				reason,
			)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func capabilityScanTargets(capability *domain.GitHubRepositoryCapability) []any {
	return []any{
		&capability.ID,
		&capability.OrgID,
		&capability.UserID,
		&capability.GitHubUserID,
		&capability.TargetEnvironment,
		&capability.IdempotencyKey,
		&capability.RequestHash,
		&capability.Status,
		&capability.GitHubInstallationID,
		&capability.GitHubRepositoryID,
		&capability.CapabilityHash,
		&capability.CapabilityCiphertext,
		&capability.CapabilityNonce,
		&capability.RepositoryOwner,
		&capability.RepositoryName,
		&capability.CreatedAt,
		&capability.UpdatedAt,
	}
}

func loadCapabilityRepository(
	ctx context.Context,
	tx pgx.Tx,
	capability *domain.GitHubRepositoryCapability,
) error {
	return tx.QueryRow(
		ctx,
		`SELECT github_repository_id, github_owner_account_id, name, full_name,
			html_url, clone_url, ssh_url, default_branch, visibility, is_private,
			is_archived, is_disabled, github_updated_at
		FROM ao_github_repositories WHERE github_repository_id = $1`,
		capability.GitHubRepositoryID,
	).Scan(
		&capability.Repository.GitHubRepositoryID,
		&capability.Repository.GitHubOwnerID,
		&capability.Repository.Name,
		&capability.Repository.FullName,
		&capability.Repository.HTMLURL,
		&capability.Repository.CloneURL,
		&capability.Repository.SSHURL,
		&capability.Repository.DefaultBranch,
		&capability.Repository.Visibility,
		&capability.Repository.IsPrivate,
		&capability.Repository.IsArchived,
		&capability.Repository.IsDisabled,
		&capability.Repository.GitHubUpdatedAt,
	)
}

func repositoryOwner(fullName string) string {
	for index, character := range fullName {
		if character == '/' {
			return fullName[:index]
		}
	}
	return ""
}
