package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// InstallationOwnedByAnotherOrgError reports that a GitHub App installation the
// caller tried to connect is already connected by a different AO organization
// under the legacy global-unique constraint, so a per-org record could not be
// created for the caller. It classifies as ErrConflict (via Is) for callers
// that only branch on the error class, while carrying the GitHub account login
// so the callback can render a specific, actionable page instead of a generic
// failure. It names the GitHub account (public information the connecting admin
// already has) rather than the owning organization, which is deliberately not
// disclosed across the tenant boundary.
type InstallationOwnedByAnotherOrgError struct {
	GitHubInstallationID int64
	AccountLogin         string
	// OwnerOrgID is the AO organization that already holds the installation. It
	// is populated for logging/diagnostics and is never rendered to the caller.
	OwnerOrgID string
}

func (e *InstallationOwnedByAnotherOrgError) Error() string {
	return fmt.Sprintf(
		"github installation %d is already connected to another organization",
		e.GitHubInstallationID,
	)
}

// Is lets errors.Is(err, ErrConflict) recognise this as a conflict so existing
// conflict handling keeps working while errors.As can still recover the owner.
func (e *InstallationOwnedByAnotherOrgError) Is(target error) bool {
	return target == ErrConflict
}

func (s *Store) CreateGitHubInstallAttempt(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	stateHash []byte,
	expiresAt time.Time,
) (domain.GitHubInstallAttempt, error) {
	var attempt domain.GitHubInstallAttempt
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_callback_routes WHERE expires_at <= now()`,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_install_attempts
			WHERE org_id = $1 AND expires_at <= now() AND consumed_at IS NULL`,
			orgID,
		); err != nil {
			return err
		}
		err := tx.QueryRow(
			ctx,
			`INSERT INTO ao_github_install_attempts (
				org_id, initiating_user_id, state_hash, expires_at
			) VALUES ($1, $2, $3, $4)
			RETURNING id, org_id, initiating_user_id, phase, expires_at`,
			orgID,
			principal.UserID,
			stateHash,
			expiresAt,
		).Scan(
			&attempt.ID,
			&attempt.OrgID,
			&attempt.InitiatingUserID,
			&attempt.Phase,
			&attempt.ExpiresAt,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO ao_github_callback_routes (
				state_hash, attempt_id, org_id, user_id, phase, expires_at
			) VALUES ($1, $2, $3, $4, 'install', $5)`,
			stateHash,
			attempt.ID,
			orgID,
			principal.UserID,
			expiresAt,
		)
		return err
	})
	return attempt, err
}

func (s *Store) ValidateGitHubInstallState(
	ctx context.Context,
	stateHash []byte,
) error {
	route, err := s.githubCallbackRoute(ctx, stateHash, "install")
	if err != nil {
		return err
	}
	return s.withGitHubOrg(ctx, route.orgID, route.userID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, route.orgID, route.userID); err != nil {
			return err
		}
		var valid bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM ao_github_install_attempts
				WHERE id = $1
				  AND phase = 'install'
				  AND consumed_at IS NULL
				  AND expires_at > now()
			)`,
			route.attemptID,
		).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) BeginGitHubOAuth(
	ctx context.Context,
	installStateHash []byte,
	installation domain.GitHubInstallation,
	oauthStateHash, verifierCiphertext, verifierNonce []byte,
	expiresAt time.Time,
) (domain.GitHubInstallAttempt, error) {
	route, err := s.githubCallbackRoute(ctx, installStateHash, "install")
	if err != nil {
		return domain.GitHubInstallAttempt{}, err
	}
	var attempt domain.GitHubInstallAttempt
	err = s.withGitHubOrg(ctx, route.orgID, route.userID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, route.orgID, route.userID); err != nil {
			return err
		}
		command, err := tx.Exec(
			ctx,
			`UPDATE ao_github_install_attempts
			SET state_hash = $1,
			    phase = 'oauth',
			    pending_github_installation_id = $2,
			    pending_github_account_id = $3,
			    pending_account_login = $4,
			    pending_account_type = $5,
			    pending_repository_selection = $6,
			    pending_recorded_at = now(),
			    oauth_verifier_ciphertext = $7,
			    oauth_verifier_nonce = $8,
			    expires_at = $9,
			    updated_at = now()
			WHERE id = $10
			  AND phase = 'install'
			  AND consumed_at IS NULL
			  AND expires_at > now()`,
			oauthStateHash,
			installation.GitHubInstallationID,
			installation.GitHubAccountID,
			installation.AccountLogin,
			installation.AccountType,
			installation.RepositorySelection,
			verifierCiphertext,
			verifierNonce,
			expiresAt,
			route.attemptID,
		)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ao_github_callback_routes WHERE state_hash = $1`, installStateHash); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_callback_routes (
				state_hash, attempt_id, org_id, user_id, phase, expires_at
			) VALUES ($1, $2, $3, $4, 'oauth', $5)`,
			oauthStateHash,
			route.attemptID,
			route.orgID,
			route.userID,
			expiresAt,
		); err != nil {
			return err
		}
		return tx.QueryRow(
			ctx,
			`SELECT id, org_id, initiating_user_id, phase,
				pending_github_installation_id,
				oauth_verifier_ciphertext, oauth_verifier_nonce, expires_at
			FROM ao_github_install_attempts
			WHERE id = $1`,
			route.attemptID,
		).Scan(
			&attempt.ID,
			&attempt.OrgID,
			&attempt.InitiatingUserID,
			&attempt.Phase,
			&attempt.PendingGitHubInstallationID,
			&attempt.OAuthVerifierCiphertext,
			&attempt.OAuthVerifierNonce,
			&attempt.ExpiresAt,
		)
	})
	return attempt, err
}

func (s *Store) GitHubOAuthAttempt(
	ctx context.Context,
	stateHash []byte,
) (domain.GitHubInstallAttempt, error) {
	route, err := s.githubCallbackRoute(ctx, stateHash, "oauth")
	if err != nil {
		return domain.GitHubInstallAttempt{}, err
	}
	var attempt domain.GitHubInstallAttempt
	err = s.withGitHubOrg(ctx, route.orgID, route.userID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, route.orgID, route.userID); err != nil {
			return err
		}
		return tx.QueryRow(
			ctx,
			`SELECT id, org_id, initiating_user_id, phase,
				pending_github_installation_id,
				oauth_verifier_ciphertext, oauth_verifier_nonce, expires_at
			FROM ao_github_install_attempts
			WHERE id = $1
			  AND phase = 'oauth'
			  AND consumed_at IS NULL
			  AND expires_at > now()`,
			route.attemptID,
		).Scan(
			&attempt.ID,
			&attempt.OrgID,
			&attempt.InitiatingUserID,
			&attempt.Phase,
			&attempt.PendingGitHubInstallationID,
			&attempt.OAuthVerifierCiphertext,
			&attempt.OAuthVerifierNonce,
			&attempt.ExpiresAt,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubInstallAttempt{}, ErrNotFound
	}
	return attempt, err
}

func (s *Store) CompleteGitHubInstallation(
	ctx context.Context,
	stateHash []byte,
	installation domain.GitHubInstallation,
) (domain.GitHubInstallation, error) {
	route, err := s.githubCallbackRoute(ctx, stateHash, "oauth")
	if err != nil {
		return domain.GitHubInstallation{}, err
	}
	err = s.withGitHubOrg(ctx, route.orgID, route.userID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, route.orgID, route.userID); err != nil {
			return err
		}
		var pendingID int64
		if err := tx.QueryRow(
			ctx,
			`SELECT pending_github_installation_id
			FROM ao_github_install_attempts
			WHERE id = $1
			  AND phase = 'oauth'
			  AND consumed_at IS NULL
			  AND expires_at > now()
			FOR UPDATE`,
			route.attemptID,
		).Scan(&pendingID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if pendingID != installation.GitHubInstallationID {
			return ErrInvalid
		}
		permissions, err := json.Marshal(installation.Permissions)
		if err != nil {
			return err
		}
		if err := s.upsertGitHubInstallationForOrg(
			ctx,
			tx,
			route.orgID,
			route.userID,
			&installation,
			permissions,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_installation_routes (
				github_installation_id, org_id, installation_id
			) VALUES ($1, $2, $3)
			ON CONFLICT (github_installation_id, org_id) DO UPDATE
			SET installation_id = EXCLUDED.installation_id`,
			installation.GitHubInstallationID,
			route.orgID,
			installation.ID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id,
				metadata
			) VALUES (
				$1, $2, 'github.installation.connected',
				'github_installation', $3,
				jsonb_build_object('githubInstallationId', $4::bigint)
			)`,
			route.orgID,
			route.userID,
			installation.ID,
			installation.GitHubInstallationID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_webhook_deliveries (
				github_delivery_id, event, action,
				github_installation_id, payload, payload_hash
			) VALUES (
				'ao-sync-' || $1::text,
				'installation_repositories',
				'initial_sync',
				$2,
				convert_to('{"source":"oauth_callback"}', 'UTF8'),
				digest(convert_to('{"source":"oauth_callback"}', 'UTF8'), 'sha256')
			)
			ON CONFLICT (github_delivery_id) DO NOTHING`,
			route.attemptID,
			installation.GitHubInstallationID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_github_install_attempts
			SET consumed_at = now(), oauth_verifier_ciphertext = NULL,
			    oauth_verifier_nonce = NULL, updated_at = now()
			WHERE id = $1`,
			route.attemptID,
		); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM ao_github_callback_routes WHERE state_hash = $1`, stateHash)
		return err
	})
	return installation, err
}

func (s *Store) BindGitHubInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	installation domain.GitHubInstallation,
) (domain.GitHubInstallation, error) {
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		permissions, err := json.Marshal(installation.Permissions)
		if err != nil {
			return err
		}
		if err := s.upsertGitHubInstallationForOrg(
			ctx,
			tx,
			orgID,
			principal.UserID,
			&installation,
			permissions,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_github_installation_routes (
				github_installation_id, org_id, installation_id
			) VALUES ($1, $2, $3)
			ON CONFLICT (github_installation_id, org_id) DO UPDATE
			SET installation_id = EXCLUDED.installation_id`,
			installation.GitHubInstallationID,
			orgID,
			installation.ID,
		); err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id,
				metadata
			) VALUES (
				$1, $2, 'github.installation.bound_for_scratch',
				'github_installation', $3,
				jsonb_build_object('githubInstallationId', $4::bigint)
			)`,
			orgID,
			principal.UserID,
			installation.ID,
			installation.GitHubInstallationID,
		)
		return err
	})
	return installation, err
}

func (s *Store) ListGitHubInstallations(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) ([]domain.GitHubInstallation, error) {
	var installations []domain.GitHubInstallation
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT id, org_id, github_installation_id, github_account_id,
				account_login, account_type, status, repository_selection,
				permissions, events, sync_status, sync_generation,
				last_synced_at, last_error,
				installed_by_user_id, created_at, updated_at
			FROM ao_github_installations
			WHERE org_id = $1
			ORDER BY created_at, id`,
			orgID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var installation domain.GitHubInstallation
			if err := rows.Scan(githubInstallationScanTargets(&installation)...); err != nil {
				return err
			}
			installations = append(installations, installation)
		}
		return rows.Err()
	})
	return installations, err
}

func (s *Store) GitHubInstallationForSync(
	ctx context.Context,
	principal domain.Principal,
	orgID, installationID string,
) (domain.GitHubInstallation, error) {
	var installation domain.GitHubInstallation
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		return scanGitHubInstallation(tx.QueryRow(
			ctx,
			`SELECT id, org_id, github_installation_id, github_account_id,
				account_login, account_type, status, repository_selection,
				permissions, events, sync_status, sync_generation,
				last_synced_at, last_error,
				installed_by_user_id, created_at, updated_at
			FROM ao_github_installations
			WHERE org_id = $1 AND id = $2 AND status = 'active'`,
			orgID,
			installationID,
		), &installation)
	})
	return installation, err
}

func (s *Store) BeginGitHubRepositorySync(
	ctx context.Context,
	installation domain.GitHubInstallation,
) (int64, error) {
	var generation int64
	err := s.withGitHubOrg(
		ctx,
		installation.OrgID,
		installation.InstalledByUserID,
		func(tx pgx.Tx) error {
			err := tx.QueryRow(
				ctx,
				`UPDATE ao_github_installations
				SET sync_status = 'syncing',
				    sync_generation = sync_generation + 1,
				    last_error = '',
				    updated_at = now()
				WHERE org_id = $1 AND id = $2 AND status = 'active'
				RETURNING sync_generation`,
				installation.OrgID,
				installation.ID,
			).Scan(&generation)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return err
		},
	)
	return generation, err
}

func (s *Store) ReconcileGitHubRepositories(
	ctx context.Context,
	orgID string,
	installation domain.GitHubInstallation,
	generation int64,
	repositories []domain.GitHubRepository,
) error {
	return s.withGitHubOrg(ctx, orgID, installation.InstalledByUserID, func(tx pgx.Tx) error {
		var status string
		var currentGeneration int64
		if err := tx.QueryRow(
			ctx,
			`SELECT status, sync_generation
			FROM ao_github_installations
			WHERE org_id = $1 AND id = $2
			FOR UPDATE`,
			orgID,
			installation.ID,
		).Scan(&status, &currentGeneration); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "active" {
			return ErrForbidden
		}
		if currentGeneration != generation {
			return ErrConflict
		}
		active := make([]int64, 0, len(repositories))
		for _, repository := range repositories {
			active = append(active, repository.GitHubRepositoryID)
			if _, err := tx.Exec(
				ctx,
				`INSERT INTO ao_github_repositories (
					github_repository_id, github_owner_account_id, name, full_name,
					html_url, clone_url, ssh_url, default_branch, visibility,
					is_private, is_archived, is_disabled, github_updated_at,
					last_synced_at
				) VALUES (
					$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now()
				)
				ON CONFLICT (github_repository_id) DO UPDATE
				SET github_owner_account_id = EXCLUDED.github_owner_account_id,
				    name = EXCLUDED.name,
				    full_name = EXCLUDED.full_name,
				    html_url = EXCLUDED.html_url,
				    clone_url = EXCLUDED.clone_url,
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
			if _, err := tx.Exec(
				ctx,
				`INSERT INTO ao_github_repository_grants (
					org_id, installation_id, github_repository_id,
					repository_selection
				) SELECT $1, $2, $3, $4
				WHERE NOT EXISTS (
					SELECT 1 FROM ao_github_repository_grants
					WHERE org_id = $1
					  AND github_repository_id = $3
					  AND revoked_at IS NULL
				)
				ON CONFLICT DO NOTHING`,
				orgID,
				installation.ID,
				repository.GitHubRepositoryID,
				installation.RepositorySelection,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_github_repository_grants
				SET installation_id = $2,
				    repository_selection = $4,
				    revoked_at = NULL,
				    revoke_reason = '',
				    last_synced_at = now()
				WHERE org_id = $1
				  AND github_repository_id = $3
				  AND revoked_at IS NULL`,
				orgID,
				installation.ID,
				repository.GitHubRepositoryID,
				installation.RepositorySelection,
			); err != nil {
				return err
			}
			// A reconnect can create a new grant row after the old installation's
			// grant was revoked. Existing projects must follow the newly verified
			// grant so their running sessions can obtain checkout credentials again.
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_projects project
				SET github_repository_grant_id = grant_row.id,
				    updated_at = now()
				FROM ao_github_repository_grants grant_row
				WHERE project.org_id = $1
				  AND project.github_repository_id = $2
				  AND project.repository_url = $3
				  AND grant_row.org_id = project.org_id
				  AND grant_row.github_repository_id = project.github_repository_id
				  AND grant_row.revoked_at IS NULL
				  AND project.github_repository_grant_id IS DISTINCT FROM grant_row.id`,
				orgID,
				repository.GitHubRepositoryID,
				repository.HTMLURL,
			); err != nil {
				return err
			}
		}
		if len(active) == 0 {
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_github_repository_grants
				SET revoked_at = now(), revoke_reason = 'repository_removed',
				    last_synced_at = now()
				WHERE org_id = $1 AND installation_id = $2 AND revoked_at IS NULL`,
				orgID,
				installation.ID,
			); err != nil {
				return err
			}
		} else if _, err := tx.Exec(
			ctx,
			`UPDATE ao_github_repository_grants
			SET revoked_at = now(), revoke_reason = 'repository_removed',
			    last_synced_at = now()
			WHERE org_id = $1
			  AND installation_id = $2
			  AND revoked_at IS NULL
			  AND NOT (github_repository_id = ANY($3))`,
			orgID,
			installation.ID,
			active,
		); err != nil {
			return err
		}
		_, err := tx.Exec(
			ctx,
			`UPDATE ao_github_installations
			SET sync_status = 'ready', last_synced_at = now(),
			    last_error = '', updated_at = now()
			WHERE org_id = $1 AND id = $2 AND sync_generation = $3`,
			orgID,
			installation.ID,
			generation,
		)
		return err
	})
}

func (s *Store) MarkGitHubSyncFailure(
	ctx context.Context,
	orgID string,
	installation domain.GitHubInstallation,
	generation int64,
	message string,
) error {
	return s.withGitHubOrg(ctx, orgID, installation.InstalledByUserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(
			ctx,
			`UPDATE ao_github_installations
			SET sync_status = 'retry', last_error = $3, updated_at = now()
			WHERE org_id = $1 AND id = $2
			  AND status = 'active'
			  AND sync_generation = $4`,
			orgID,
			installation.ID,
			boundedError(message),
			generation,
		)
		return err
	})
}

func (s *Store) DisconnectGitHubInstallation(
	ctx context.Context,
	principal domain.Principal,
	orgID, installationID string,
) (domain.GitHubInstallation, error) {
	var installation domain.GitHubInstallation
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_github_installations
			SET status = 'disconnected',
			    disconnected_at = now(),
			    sync_generation = sync_generation + 1,
			    updated_at = now()
			WHERE org_id = $1 AND id = $2 AND status <> 'deleted'`,
			orgID,
			installationID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_github_repository_grants
			SET revoked_at = COALESCE(revoked_at, now()),
			    revoke_reason = CASE WHEN revoked_at IS NULL THEN 'installation_disconnected' ELSE revoke_reason END
			WHERE org_id = $1 AND installation_id = $2`,
			orgID,
			installationID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_github_installation_routes
			WHERE org_id = $1 AND installation_id = $2`,
			orgID,
			installationID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id
			) VALUES (
				$1, $2, 'github.installation.disconnected',
				'github_installation', $3
			)`,
			orgID,
			principal.UserID,
			installationID,
		); err != nil {
			return err
		}
		return scanGitHubInstallation(tx.QueryRow(
			ctx,
			`SELECT id, org_id, github_installation_id, github_account_id,
				account_login, account_type, status, repository_selection,
				permissions, events, sync_status, sync_generation,
				last_synced_at, last_error,
				installed_by_user_id, created_at, updated_at
			FROM ao_github_installations WHERE org_id = $1 AND id = $2`,
			orgID,
			installationID,
		), &installation)
	})
	return installation, err
}

func (s *Store) ListGitHubRepositories(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.GitHubRepository, bool, error) {
	var repositories []domain.GitHubRepository
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		cursorTime := time.Now().UTC().Add(time.Hour)
		cursorID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
		if cursor != nil {
			cursorTime = cursor.Time
			cursorID = cursor.ID
		}
		rows, err := tx.Query(
			ctx,
			`WITH latest_grant AS (
				SELECT DISTINCT ON (github_repository_id)
					id, github_repository_id, granted_at, revoked_at
				FROM ao_github_repository_grants
				WHERE org_id = $1
				ORDER BY github_repository_id, granted_at DESC, id DESC
			)
			SELECT grant_row.id, repository.github_repository_id,
				repository.github_owner_account_id, repository.name,
				repository.full_name, repository.html_url, repository.clone_url,
				repository.ssh_url, repository.default_branch,
				repository.visibility, repository.is_private,
				repository.is_archived, repository.is_disabled,
				repository.github_updated_at, grant_row.granted_at,
				grant_row.revoked_at
			FROM latest_grant grant_row
			JOIN ao_github_repositories repository
			  ON repository.github_repository_id = grant_row.github_repository_id
			WHERE (grant_row.granted_at, grant_row.id) < ($2, $3)
			ORDER BY grant_row.granted_at DESC, grant_row.id DESC
			LIMIT $4`,
			orgID,
			cursorTime,
			cursorID,
			limit+1,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var repository domain.GitHubRepository
			if err := rows.Scan(
				&repository.GrantID,
				&repository.GitHubRepositoryID,
				&repository.GitHubOwnerID,
				&repository.Name,
				&repository.FullName,
				&repository.HTMLURL,
				&repository.CloneURL,
				&repository.SSHURL,
				&repository.DefaultBranch,
				&repository.Visibility,
				&repository.IsPrivate,
				&repository.IsArchived,
				&repository.IsDisabled,
				&repository.GitHubUpdatedAt,
				&repository.GrantedAt,
				&repository.RevokedAt,
			); err != nil {
				return err
			}
			repositories = append(repositories, repository)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(repositories) > limit
	if hasMore {
		repositories = repositories[:limit]
	}
	return repositories, hasMore, nil
}

// GitHubInstallationForRepository resolves the active installation backing
// one of an organization's granted repositories, keyed by full_name
// (owner/repo) rather than by session — background pull-request status
// polling must keep refreshing after the session that raised a PR has
// terminated, since the PR itself lives on until closed or merged.
func (s *Store) GitHubInstallationForRepository(
	ctx context.Context,
	orgID, repository string,
) (installationID, repositoryID int64, err error) {
	err = s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`WITH latest_grant AS (
				SELECT DISTINCT ON (github_repository_id)
					id, github_repository_id, installation_id, revoked_at
				FROM ao_github_repository_grants
				WHERE org_id = $1
				ORDER BY github_repository_id, granted_at DESC, id DESC
			)
			SELECT installation.github_installation_id, repository.github_repository_id
			FROM ao_github_repositories repository
			JOIN latest_grant grant_row
			  ON grant_row.github_repository_id = repository.github_repository_id
			JOIN ao_github_installations installation
			  ON installation.org_id = $1 AND installation.id = grant_row.installation_id
			WHERE repository.full_name = $2
			  AND grant_row.revoked_at IS NULL
			  AND installation.status = 'active'
			  AND installation.suspended_at IS NULL
			  AND installation.disconnected_at IS NULL
			  AND installation.deleted_at IS NULL
			  AND repository.is_archived = false
			  AND repository.is_disabled = false`,
			orgID, repository,
		).Scan(&installationID, &repositoryID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	return installationID, repositoryID, nil
}

func (s *Store) CreateGitHubProject(
	ctx context.Context,
	principal domain.Principal,
	orgID, idempotencyKey string,
	input domain.CreateGitHubProject,
) (domain.Project, error) {
	var project domain.Project
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		var commandID string
		err = tx.QueryRow(
			ctx,
			`INSERT INTO ao_commands (
				org_id, idempotency_key, kind, payload
			) VALUES ($1, $2, 'github.project.create', $3)
			ON CONFLICT (org_id, idempotency_key) DO NOTHING
			RETURNING id`,
			orgID,
			idempotencyKey,
			payload,
		).Scan(&commandID)
		if errors.Is(err, pgx.ErrNoRows) {
			return loadIdempotentProject(
				ctx,
				tx,
				orgID,
				idempotencyKey,
				"github.project.create",
				payload,
				&project,
			)
		}
		if err != nil {
			return err
		}
		config := input.Config
		if len(config) == 0 {
			config = json.RawMessage(`{}`)
		}
		err = scanProject(tx.QueryRow(
			ctx,
			`INSERT INTO ao_projects (
				org_id, display_name, repository_url, default_branch, config,
				github_repository_id, github_repository_grant_id
			)
			SELECT $1,
				COALESCE(NULLIF($2, ''), repository.name),
				repository.html_url,
				repository.default_branch,
				$3,
				repository.github_repository_id,
				grant_row.id
			FROM ao_github_repository_grants grant_row
			JOIN ao_github_repositories repository
			  ON repository.github_repository_id = grant_row.github_repository_id
			WHERE grant_row.org_id = $1
			  AND grant_row.github_repository_id = $4
			  AND grant_row.revoked_at IS NULL
			RETURNING id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at`,
			orgID,
			input.DisplayName,
			config,
			input.GitHubRepositoryID,
		), &project)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			// The App path authorizes by repository id and derives the URL
			// server-side, so it is not available here; the API renders a
			// repository-agnostic message when the org already has a project for
			// this repository.
			return projectRepositoryConflict(err, "")
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_commands
			SET status = 'succeeded',
			    result = jsonb_build_object('projectId', $1::text),
			    updated_at = now()
			WHERE id = $2`,
			project.ID,
			commandID,
		); err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id,
				metadata
			) VALUES (
				$1, $2, 'github_project.created', 'project', $3,
				jsonb_build_object('githubRepositoryId', $4::bigint)
			)`,
			orgID,
			principal.UserID,
			project.ID,
			input.GitHubRepositoryID,
		)
		return err
	})
	return project, err
}

func (s *Store) InsertGitHubWebhook(
	ctx context.Context,
	delivery domain.GitHubWebhookDelivery,
	payloadHash []byte,
) (bool, error) {
	command, err := s.pool.Exec(
		ctx,
		`INSERT INTO ao_github_webhook_deliveries (
			github_delivery_id, event, action, github_installation_id,
			github_repository_id, payload, payload_hash
		) VALUES (
			$1, $2, $3, NULLIF($4::bigint, 0), NULLIF($5::bigint, 0), $6, $7
		)
		ON CONFLICT (github_delivery_id) DO NOTHING`,
		delivery.DeliveryID,
		delivery.Event,
		delivery.Action,
		delivery.GitHubInstallationID,
		delivery.GitHubRepositoryID,
		delivery.Payload,
		payloadHash,
	)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 1 {
		return true, nil
	}
	var existing []byte
	var existingEvent, existingAction string
	var existingInstallationID, existingRepositoryID int64
	if err := s.pool.QueryRow(
		ctx,
		`SELECT payload_hash, event, action,
			COALESCE(github_installation_id, 0),
			COALESCE(github_repository_id, 0)
		FROM ao_github_webhook_deliveries WHERE github_delivery_id = $1`,
		delivery.DeliveryID,
	).Scan(
		&existing,
		&existingEvent,
		&existingAction,
		&existingInstallationID,
		&existingRepositoryID,
	); err != nil {
		return false, err
	}
	if !bytes.Equal(existing, payloadHash) ||
		existingEvent != delivery.Event ||
		existingAction != delivery.Action ||
		existingInstallationID != delivery.GitHubInstallationID ||
		existingRepositoryID != delivery.GitHubRepositoryID {
		return false, ErrConflict
	}
	return false, nil
}

// RecentOpenedPullRequestWebhooks replays verified GitHub facts when a worker
// reports a custom branch after its opened webhook was already processed.
func (s *Store) RecentOpenedPullRequestWebhooks(ctx context.Context, repositoryID int64, since time.Time, limit int) ([]domain.GitHubWebhookDelivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `SELECT github_delivery_id, event, action,
		COALESCE(github_installation_id, 0), COALESCE(github_repository_id, 0), payload
		FROM ao_github_webhook_deliveries
		WHERE github_repository_id = $1 AND event = 'pull_request' AND action = 'opened'
		  AND received_at >= $2
		ORDER BY received_at DESC LIMIT $3`, repositoryID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []domain.GitHubWebhookDelivery
	for rows.Next() {
		var delivery domain.GitHubWebhookDelivery
		if err := rows.Scan(&delivery.DeliveryID, &delivery.Event, &delivery.Action,
			&delivery.GitHubInstallationID, &delivery.GitHubRepositoryID, &delivery.Payload); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Store) ClaimGitHubWebhook(
	ctx context.Context,
	owner string,
	leaseUntil time.Time,
) (domain.GitHubWebhookDelivery, error) {
	var delivery domain.GitHubWebhookDelivery
	err := s.pool.QueryRow(
		ctx,
		`WITH candidate AS (
			SELECT candidate_row.github_delivery_id
			FROM ao_github_webhook_deliveries candidate_row
			WHERE (
				(
					candidate_row.status IN ('pending', 'retry')
					AND COALESCE(candidate_row.next_attempt_at, candidate_row.received_at) <= now()
				)
				OR (
					candidate_row.status = 'processing'
					AND (
						candidate_row.lease_until IS NULL
						OR candidate_row.lease_until < now()
					)
				)
			)
			  AND (
				candidate_row.lease_until IS NULL
				OR candidate_row.lease_until < now()
			  )
			  -- SCM webhooks refresh current GitHub state, so a retrying PR must
			  -- not hold every later event for the same installation. Installation
			  -- changes still run in receipt order because they mutate routing.
			  AND NOT EXISTS (
				SELECT 1
				FROM ao_github_webhook_deliveries earlier
				WHERE candidate_row.event IN ('installation', 'installation_repositories')
				  AND earlier.event IN ('installation', 'installation_repositories')
				  AND candidate_row.github_installation_id IS NOT NULL
				  AND earlier.github_installation_id = candidate_row.github_installation_id
				  AND earlier.status IN ('pending', 'retry', 'processing')
				  AND (earlier.received_at, earlier.github_delivery_id) <
				      (candidate_row.received_at, candidate_row.github_delivery_id)
			  )
			ORDER BY
				COALESCE(candidate_row.next_attempt_at, candidate_row.received_at),
				candidate_row.received_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE ao_github_webhook_deliveries delivery
		SET status = 'processing', lease_owner = $1, lease_until = $2,
		    processing_started_at = now(), last_attempt_at = now(),
		    attempt_count = attempt_count + 1, updated_at = now()
		FROM candidate
		WHERE delivery.github_delivery_id = candidate.github_delivery_id
		RETURNING delivery.github_delivery_id, delivery.event, delivery.action,
			COALESCE(delivery.github_installation_id, 0),
			COALESCE(delivery.github_repository_id, 0),
			delivery.payload, delivery.attempt_count`,
		owner,
		leaseUntil,
	).Scan(
		&delivery.DeliveryID,
		&delivery.Event,
		&delivery.Action,
		&delivery.GitHubInstallationID,
		&delivery.GitHubRepositoryID,
		&delivery.Payload,
		&delivery.AttemptCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GitHubWebhookDelivery{}, ErrNotFound
	}
	return delivery, err
}

func (s *Store) CompleteGitHubWebhook(ctx context.Context, deliveryID, owner string) error {
	command, err := s.pool.Exec(
		ctx,
		`UPDATE ao_github_webhook_deliveries
		SET status = 'processed', processed_at = now(), lease_owner = '',
		    lease_until = NULL, last_error = '', updated_at = now()
		WHERE github_delivery_id = $1 AND lease_owner = $2`,
		deliveryID,
		owner,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) RetryGitHubWebhook(
	ctx context.Context,
	deliveryID, owner, message string,
	retryAt time.Time,
	terminal bool,
) error {
	status := "retry"
	if terminal {
		status = "failed"
	}
	command, err := s.pool.Exec(
		ctx,
		`UPDATE ao_github_webhook_deliveries
		SET status = $3, next_attempt_at = $4, lease_owner = '',
		    lease_until = NULL, last_error = $5, last_error_at = now(),
		    updated_at = now()
		WHERE github_delivery_id = $1 AND lease_owner = $2`,
		deliveryID,
		owner,
		status,
		retryAt,
		boundedError(message),
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// GitHubInstallationRoutes returns every organization route for a GitHub App
// installation. Because an installation may be connected by multiple
// organizations, a webhook delivery must fan out to each one; the caller
// iterates the returned routes. It returns ErrNotFound when no organization
// routes the installation.
func (s *Store) GitHubInstallationRoutes(
	ctx context.Context,
	githubInstallationID int64,
) ([]domain.GitHubInstallationRoute, error) {
	rows, err := s.pool.Query(
		ctx,
		`SELECT org_id, installation_id
		FROM ao_github_installation_routes
		WHERE github_installation_id = $1
		ORDER BY created_at, org_id`,
		githubInstallationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []domain.GitHubInstallationRoute
	for rows.Next() {
		var route domain.GitHubInstallationRoute
		if err := rows.Scan(&route.OrgID, &route.InstallationID); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, ErrNotFound
	}
	return routes, nil
}

func (s *Store) GitHubInstallationByRoute(
	ctx context.Context,
	orgID, installationID string,
) (domain.GitHubInstallation, error) {
	var installation domain.GitHubInstallation
	err := s.withGitHubOrg(ctx, orgID, "", func(tx pgx.Tx) error {
		return scanGitHubInstallation(tx.QueryRow(
			ctx,
			`SELECT id, org_id, github_installation_id, github_account_id,
				account_login, account_type, status, repository_selection,
				permissions, events, sync_status, sync_generation,
				last_synced_at, last_error,
				installed_by_user_id, created_at, updated_at
			FROM ao_github_installations WHERE org_id = $1 AND id = $2`,
			orgID,
			installationID,
		), &installation)
	})
	return installation, err
}

func (s *Store) ApplyGitHubInstallationEvent(
	ctx context.Context,
	orgID, installationID, action, source string,
) error {
	if source != "webhook" && source != "reconcile" {
		return ErrInvalid
	}
	return s.withGitHubOrg(ctx, orgID, "", func(tx pgx.Tx) error {
		status := "active"
		switch action {
		case "suspend":
			status = "suspended"
		case "deleted":
			status = "deleted"
		case "unsuspend", "new_permissions_accepted", "created":
		default:
			return ErrInvalid
		}
		command, err := tx.Exec(
			ctx,
			`UPDATE ao_github_installations
			SET status = $3,
			    suspended_at = CASE WHEN $3 = 'suspended' THEN now() ELSE NULL END,
			    deleted_at = CASE WHEN $3 = 'deleted' THEN now() ELSE deleted_at END,
			    sync_status = CASE WHEN $3 = 'active' THEN 'pending' ELSE sync_status END,
			    sync_generation = sync_generation + 1,
			    updated_at = now()
			WHERE org_id = $1 AND id = $2
			  AND (
				status NOT IN ('disconnected', 'deleted')
				OR $3 = 'deleted'
			  )`,
			orgID,
			installationID,
			status,
		)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return nil
		}
		if status == "active" {
			_, err = tx.Exec(
				ctx,
				`INSERT INTO ao_audit_events (
					org_id, action, resource_type, resource_id, metadata
				) VALUES (
					$1, 'github.installation.' || $3,
					'github_installation', $2,
					jsonb_build_object('source', $4::text)
				)`,
				orgID,
				installationID,
				action,
				source,
			)
			return err
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_github_repository_grants
			SET revoked_at = COALESCE(revoked_at, now()),
			    revoke_reason = CASE
			        WHEN revoked_at IS NULL THEN 'installation_' || $3
			        ELSE revoke_reason
			    END
			WHERE org_id = $1 AND installation_id = $2`,
			orgID,
			installationID,
			status,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, action, resource_type, resource_id, metadata
			) VALUES (
				$1, 'github.installation.' || $3,
				'github_installation', $2,
				jsonb_build_object('source', $4::text)
			)`,
			orgID,
			installationID,
			action,
			source,
		)
		return err
	})
}

type githubCallbackRoute struct {
	attemptID string
	orgID     string
	userID    string
}

func (s *Store) githubCallbackRoute(
	ctx context.Context,
	stateHash []byte,
	phase string,
) (githubCallbackRoute, error) {
	var route githubCallbackRoute
	err := s.pool.QueryRow(
		ctx,
		`SELECT attempt_id, org_id, user_id
		FROM ao_github_callback_routes
		WHERE state_hash = $1 AND phase = $2 AND expires_at > now()`,
		stateHash,
		phase,
	).Scan(&route.attemptID, &route.orgID, &route.userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubCallbackRoute{}, ErrNotFound
	}
	return route, err
}

func (s *Store) withGitHubOrg(
	ctx context.Context,
	orgID, userID string,
	fn tenantFn,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		userID,
		orgID,
	); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func requireOrgAdmin(ctx context.Context, tx pgx.Tx, orgID, userID string) error {
	var role string
	if err := tx.QueryRow(
		ctx,
		`SELECT role FROM ao_org_memberships
		WHERE org_id = $1 AND user_id = $2 AND status = 'active'`,
		orgID,
		userID,
	).Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	return nil
}

func githubInstallationScanTargets(installation *domain.GitHubInstallation) []any {
	return []any{
		&installation.ID,
		&installation.OrgID,
		&installation.GitHubInstallationID,
		&installation.GitHubAccountID,
		&installation.AccountLogin,
		&installation.AccountType,
		&installation.Status,
		&installation.RepositorySelection,
		&installation.Permissions,
		&installation.Events,
		&installation.SyncStatus,
		&installation.SyncGeneration,
		&installation.LastSyncedAt,
		&installation.LastError,
		&installation.InstalledByUserID,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	}
}

func scanGitHubInstallation(row pgx.Row, installation *domain.GitHubInstallation) error {
	err := row.Scan(githubInstallationScanTargets(installation)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// githubInstallationColumns is the column list every installation query returns,
// matching the order of githubInstallationScanTargets.
const githubInstallationColumns = `id, org_id, github_installation_id, github_account_id,
	account_login, account_type, status, repository_selection,
	permissions, events, sync_status, sync_generation,
	last_synced_at, last_error,
	installed_by_user_id, created_at, updated_at`

// upsertGitHubInstallationForOrg reconnects the organization's own installation
// row for github_installation_id, or inserts a fresh per-org record when the
// organization has none. Several organizations may each hold their own row for
// one github_installation_id: they share the underlying GitHub installation
// (and its org-agnostic access tokens) but keep isolated AO sessions, projects
// and repository grants. Only a caller that has already proven the connecting
// user is a GitHub admin of the installation reaches here.
//
// If a fresh row cannot be inserted because a different organization already
// holds the installation under the legacy global-unique constraint (for example
// migration 00055 has not been applied, or the relaxation was rolled back), it
// returns *InstallationOwnedByAnotherOrgError so the callback can name the
// GitHub account and tell the user it is connected elsewhere, instead of a
// generic failure.
func (s *Store) upsertGitHubInstallationForOrg(
	ctx context.Context,
	tx pgx.Tx,
	orgID, userID string,
	installation *domain.GitHubInstallation,
	permissions []byte,
) error {
	err := tx.QueryRow(
		ctx,
		`UPDATE ao_github_installations
		SET account_login = $3,
		    account_type = $4,
		    status = 'active',
		    repository_selection = $5,
		    permissions = $6,
		    events = $7,
		    sync_status = 'pending',
		    sync_generation = sync_generation + 1,
		    suspended_at = NULL,
		    disconnected_at = NULL,
		    deleted_at = NULL,
		    updated_at = now()
		WHERE org_id = $1 AND github_installation_id = $2
		RETURNING `+githubInstallationColumns,
		orgID,
		installation.GitHubInstallationID,
		installation.AccountLogin,
		installation.AccountType,
		installation.RepositorySelection,
		permissions,
		installation.Events,
	).Scan(githubInstallationScanTargets(installation)...)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	err = tx.QueryRow(
		ctx,
		`INSERT INTO ao_github_installations (
			org_id, github_installation_id, github_account_id,
			account_login, account_type, status, repository_selection,
			permissions, events, installed_by_user_id, sync_status
		) VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $8, $9, 'pending')
		RETURNING `+githubInstallationColumns,
		orgID,
		installation.GitHubInstallationID,
		installation.GitHubAccountID,
		installation.AccountLogin,
		installation.AccountType,
		installation.RepositorySelection,
		permissions,
		installation.Events,
		userID,
	).Scan(githubInstallationScanTargets(installation)...)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		// The insert violated a unique constraint. Under the multi-org schema a
		// (org_id, github_installation_id) pair is unique, so a violation here
		// means another organization already holds this installation under the
		// legacy global-unique constraint. Consult the non-RLS routes table on a
		// fresh pool connection (this transaction is now aborted) to identify the
		// owner and return a specific, actionable error.
		if owner, ok := s.installationOwnedByAnotherOrg(
			ctx,
			installation.GitHubInstallationID,
			orgID,
		); ok {
			return &InstallationOwnedByAnotherOrgError{
				GitHubInstallationID: installation.GitHubInstallationID,
				AccountLogin:         installation.AccountLogin,
				OwnerOrgID:           owner,
			}
		}
		return ErrConflict
	}
	return err
}

// installationOwnedByAnotherOrg reports whether a different organization already
// routes the installation. It reads ao_github_installation_routes, which is not
// row-level-security scoped, on a pool connection so it works even when the
// caller's transaction has been aborted by a failed insert.
func (s *Store) installationOwnedByAnotherOrg(
	ctx context.Context,
	githubInstallationID int64,
	excludeOrgID string,
) (string, bool) {
	var ownerOrgID string
	if err := s.pool.QueryRow(
		ctx,
		`SELECT org_id
		FROM ao_github_installation_routes
		WHERE github_installation_id = $1 AND org_id <> $2
		ORDER BY created_at
		LIMIT 1`,
		githubInstallationID,
		excludeOrgID,
	).Scan(&ownerOrgID); err != nil {
		return "", false
	}
	return ownerOrgID, true
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func boundedError(value string) string {
	const max = 1000
	if len(value) <= max {
		return value
	}
	return value[:max]
}
