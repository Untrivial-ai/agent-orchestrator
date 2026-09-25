package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateProject(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	idempotencyKey string,
	input domain.CreateProject,
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
			) VALUES ($1, $2, 'project.create', $3)
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
				"project.create",
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
				org_id, display_name, repository_url, default_branch, config
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at`,
			orgID,
			input.DisplayName,
			input.RepositoryURL,
			input.DefaultBranch,
			config,
		), &project)
		if err != nil {
			return normalizeConstraintError(err)
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
				org_id, actor_user_id, action, resource_type, resource_id
			) VALUES ($1, $2, 'project.created', 'project', $3)`,
			orgID,
			principal.UserID,
			project.ID,
		)
		return err
	})
	return project, err
}

func (s *Store) UpdateProject(
	ctx context.Context,
	principal domain.Principal,
	orgID, projectID string,
	input domain.UpdateProject,
) (domain.Project, error) {
	var project domain.Project
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		err := scanProject(tx.QueryRow(ctx,
			`UPDATE ao_projects
			SET display_name = $1,
				default_branch = $2,
				updated_at = now()
			WHERE org_id = $3 AND id = $4 AND archived_at IS NULL
			RETURNING id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at`,
			input.DisplayName,
			input.DefaultBranch,
			orgID,
			projectID,
		), &project)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return normalizeConstraintError(err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id
			) VALUES ($1, $2, 'project.updated', 'project', $3)`,
			orgID,
			principal.UserID,
			projectID,
		)
		return err
	})
	return project, err
}

func (s *Store) ArchiveProject(
	ctx context.Context,
	principal domain.Principal,
	orgID, projectID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var archived bool
		if err := tx.QueryRow(ctx,
			`SELECT archived_at IS NOT NULL
			FROM ao_projects
			WHERE org_id = $1 AND id = $2
			FOR UPDATE`,
			orgID,
			projectID,
		).Scan(&archived); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if archived {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE ao_projects
			SET archived_at = now(), updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID,
			projectID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE ao_sandboxes sandbox
			SET desired_state = $3,
				startup_started_at = NULL,
				reconcile_after = now(),
				updated_at = now()
			FROM ao_sessions session
			WHERE session.org_id = $1
			  AND session.project_id = $2
			  AND sandbox.org_id = session.org_id
			  AND sandbox.session_id = session.id
			  AND sandbox.observed_state <> $3`,
			orgID,
			projectID,
			domain.SandboxDesiredDeleted,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id
			) VALUES ($1, $2, 'project.deleted', 'project', $3)`,
			orgID,
			principal.UserID,
			projectID,
		)
		return err
	})
}

func loadIdempotentProject(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	idempotencyKey string,
	expectedKind string,
	payload []byte,
	project *domain.Project,
) error {
	var storedPayload []byte
	var projectID string
	var kind, status string
	err := tx.QueryRow(
		ctx,
		`SELECT kind, status, payload, result->>'projectId'
		FROM ao_commands
		WHERE org_id = $1 AND idempotency_key = $2`,
		orgID,
		idempotencyKey,
	).Scan(&kind, &status, &storedPayload, &projectID)
	if err != nil {
		return err
	}
	if kind != expectedKind || status != "succeeded" ||
		!jsonEqual(storedPayload, payload) || projectID == "" {
		return ErrIdempotencyMismatch
	}
	return scanProject(tx.QueryRow(
		ctx,
		`SELECT id, org_id, display_name, repository_url, default_branch,
			github_repository_id, config, created_at, updated_at
		FROM ao_projects WHERE org_id = $1 AND id = $2`,
		orgID,
		projectID,
	), project)
}

func (s *Store) ListProjects(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.Project, bool, error) {
	var projects []domain.Project
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at
			FROM ao_projects
			WHERE org_id = $1
			  AND archived_at IS NULL
			  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
			ORDER BY created_at DESC, id DESC
			LIMIT $4`,
			orgID,
			cursorTime(cursor),
			cursorID(cursor),
			limit+1,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var project domain.Project
			if err := scanProject(rows, &project); err != nil {
				return err
			}
			projects = append(projects, project)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(projects) > limit
	if hasMore {
		projects = projects[:limit]
	}
	return projects, hasMore, nil
}

// GetProject returns one project by id (tenant-scoped). Used at session creation
// to read the project's coder dev-kit config so each session inherits the
// template/size/startup/extra-repos chosen at project setup.
func (s *Store) GetProject(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	projectID string,
) (domain.Project, error) {
	var project domain.Project
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		scanErr := scanProject(tx.QueryRow(
			ctx,
			`SELECT id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at
			FROM ao_projects
			WHERE org_id = $1 AND id = $2 AND archived_at IS NULL`,
			orgID,
			projectID,
		), &project)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return scanErr
	})
	return project, err
}

func (s *Store) CreateSession(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	idempotencyKey string,
	maxActiveSandboxes int,
	input domain.CreateSession,
) (domain.Session, error) {
	var session domain.Session
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var err error
		session, err = createSessionTx(
			ctx, tx, orgID, idempotencyKey, maxActiveSandboxes, input, input.ParentSessionID, principal.UserID,
		)
		if err != nil || input.PreparationExpiresAfter <= 0 {
			return err
		}
		return tx.QueryRow(
			ctx,
			`SELECT sandbox.preparation_generation,
				COALESCE(command.result->>'disposition', 'created')
			FROM ao_sandboxes sandbox
			JOIN ao_commands command
			  ON command.org_id = sandbox.org_id
			 AND command.idempotency_key = $3
			WHERE sandbox.org_id = $1 AND sandbox.session_id = $2`,
			orgID, session.ID, idempotencyKey,
		).Scan(&session.PreparationGeneration, &session.PreparationDisposition)
	})
	return session, err
}

func (s *Store) CommitSessionPreparation(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, idempotencyKey string,
	input domain.CommitSessionPreparation,
) (domain.Session, error) {
	var session domain.Session
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		payload, err := json.Marshal(struct {
			SessionID string                          `json:"sessionId"`
			Input     domain.CommitSessionPreparation `json:"input"`
		}{SessionID: sessionID, Input: input})
		if err != nil {
			return err
		}
		const commandKind = "session.preparation.commit"
		var commandID string
		err = tx.QueryRow(
			ctx,
			`INSERT INTO ao_commands (
				org_id, idempotency_key, kind, payload
			) VALUES ($1, $2, $3, $4)
			ON CONFLICT (org_id, idempotency_key) DO NOTHING
			RETURNING id`,
			orgID, idempotencyKey, commandKind, payload,
		).Scan(&commandID)
		if errors.Is(err, pgx.ErrNoRows) {
			return loadIdempotentSession(
				ctx, tx, orgID, idempotencyKey, payload, commandKind, &session,
			)
		}
		if err != nil {
			return err
		}

		var preparation, terminated, expired, attached bool
		var desiredState string
		var generation int64
		err = tx.QueryRow(
			ctx,
			`SELECT session.is_preparation, session.is_terminated,
				COALESCE(
					session.preparation_expires_at <= clock_timestamp()
					OR sandbox.preparation_expires_at <= clock_timestamp(),
					true
				),
				sandbox.desired_state,
				sandbox.preparation_generation,
				EXISTS (
					SELECT 1 FROM ao_preparation_attachments attachment
					WHERE attachment.org_id = session.org_id
					  AND attachment.session_id = session.id
					  AND attachment.client_instance_id = $4
					  AND attachment.generation = $5
					  AND attachment.detached_at IS NULL
					  AND attachment.lease_expires_at > clock_timestamp()
				)
			FROM ao_sessions session
			JOIN ao_sandboxes sandbox
			  ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
			WHERE session.org_id = $1 AND session.id = $2
			  AND session.created_by_user_id = $3
			FOR UPDATE OF session, sandbox`,
			orgID, sessionID, principal.UserID, input.ClientInstanceID, input.Generation,
		).Scan(&preparation, &terminated, &expired, &desiredState, &generation, &attached)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !preparation {
			return ErrPreparationCommitted
		}
		if expired {
			return ErrPreparationExpired
		}
		if generation != input.Generation || !attached {
			return ErrPreparationStale
		}
		if terminated || desiredState != "running" {
			return ErrPreparationUnavailable
		}

		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sessions
			SET display_name = $3,
				is_preparation = false,
				preparation_expires_at = NULL,
				updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, sessionID, input.DisplayName,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM ao_preparation_attachments
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET preparation_expires_at = NULL,
				updated_at = now()
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID,
		); err != nil {
			return err
		}
		if _, err := appendUserMessage(ctx, tx, orgID, sessionID, input.Prompt, 0, "", nil); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_commands
			SET session_id = $1, status = 'succeeded',
				result = jsonb_build_object('sessionId', $2::text),
				updated_at = now()
			WHERE id = $3`,
			sessionID, sessionID, commandID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_audit_events (
				org_id, actor_user_id, action, resource_type, resource_id
			) VALUES ($1, $2, 'session.preparation.committed', 'session', $3)`,
			orgID, principal.UserID, sessionID,
		); err != nil {
			return err
		}
		return getSession(ctx, tx, orgID, sessionID, &session)
	})
	return session, err
}

func (s *Store) RenewSessionPreparation(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, clientInstanceID string,
	generation int64,
	lease time.Duration,
) (domain.SessionPreparationLease, error) {
	var result domain.SessionPreparationLease
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var preparation, terminated, expired, attached bool
		var desiredState string
		var currentGeneration int64
		err := tx.QueryRow(
			ctx,
			`SELECT session.is_preparation, session.is_terminated,
				COALESCE(
					session.preparation_expires_at <= clock_timestamp()
					OR sandbox.preparation_expires_at <= clock_timestamp(),
					true
				),
				sandbox.desired_state,
				sandbox.preparation_generation,
				EXISTS (
					SELECT 1 FROM ao_preparation_attachments attachment
					WHERE attachment.org_id = session.org_id
					  AND attachment.session_id = session.id
					  AND attachment.client_instance_id = $4
					  AND attachment.generation = $5
					  AND attachment.detached_at IS NULL
					  AND attachment.lease_expires_at > clock_timestamp()
				)
			FROM ao_sessions session
			JOIN ao_sandboxes sandbox
			  ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
			WHERE session.org_id = $1 AND session.id = $2
			  AND session.created_by_user_id = $3
			FOR UPDATE OF session, sandbox`,
			orgID, sessionID, principal.UserID, clientInstanceID, generation,
		).Scan(&preparation, &terminated, &expired, &desiredState, &currentGeneration, &attached)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !preparation {
			return ErrPreparationCommitted
		}
		if expired {
			return ErrPreparationExpired
		}
		if currentGeneration != generation || !attached {
			return ErrPreparationStale
		}
		if terminated || desiredState != "running" {
			return ErrPreparationUnavailable
		}
		if err := tx.QueryRow(
			ctx,
			`SELECT clock_timestamp() + $1::interval`,
			intervalString(lease),
		).Scan(&result.ExpiresAt); err != nil {
			return err
		}
		result.Generation = currentGeneration
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sessions
			SET preparation_expires_at = $3, updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, sessionID, result.ExpiresAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET preparation_expires_at = $3, updated_at = now()
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID, result.ExpiresAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_preparation_attachments
			SET last_activity_at = now(),
				lease_expires_at = $5
			WHERE org_id = $1 AND session_id = $2
			  AND client_instance_id = $3 AND generation = $4`,
			orgID, sessionID, clientInstanceID, generation, result.ExpiresAt,
		); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func (s *Store) DetachSessionPreparation(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, clientInstanceID string,
	generation int64,
	grace time.Duration,
) (domain.SessionPreparationLease, error) {
	var result domain.SessionPreparationLease
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var preparation, terminated, expired, attached bool
		var desiredState string
		var currentGeneration int64
		err := tx.QueryRow(
			ctx,
			`SELECT session.is_preparation, session.is_terminated,
				COALESCE(
					session.preparation_expires_at <= clock_timestamp()
					OR sandbox.preparation_expires_at <= clock_timestamp(),
					true
				),
				sandbox.desired_state,
				sandbox.preparation_generation,
				EXISTS (
					SELECT 1 FROM ao_preparation_attachments attachment
					WHERE attachment.org_id = session.org_id
					  AND attachment.session_id = session.id
					  AND attachment.client_instance_id = $4
					  AND attachment.generation = $5
				)
			FROM ao_sessions session
			JOIN ao_sandboxes sandbox
			  ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
			WHERE session.org_id = $1 AND session.id = $2
			  AND session.created_by_user_id = $3
			FOR UPDATE OF session, sandbox`,
			orgID, sessionID, principal.UserID, clientInstanceID, generation,
		).Scan(&preparation, &terminated, &expired, &desiredState, &currentGeneration, &attached)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !preparation {
			return ErrPreparationCommitted
		}
		if expired {
			return ErrPreparationExpired
		}
		if currentGeneration != generation || !attached {
			return ErrPreparationStale
		}
		if terminated || desiredState != domain.SandboxDesiredRunning {
			return ErrPreparationUnavailable
		}
		if err := tx.QueryRow(
			ctx,
			`SELECT clock_timestamp() + $1::interval`,
			intervalString(grace),
		).Scan(&result.ExpiresAt); err != nil {
			return err
		}
		result.Generation = currentGeneration
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sessions
			SET preparation_expires_at = $3, updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, sessionID, result.ExpiresAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET preparation_expires_at = $3, updated_at = now()
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID, result.ExpiresAt,
		); err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_preparation_attachments
			SET detached_at = COALESCE(detached_at, now()),
				lease_expires_at = $5
			WHERE org_id = $1 AND session_id = $2
			  AND client_instance_id = $3 AND generation = $4`,
			orgID, sessionID, clientInstanceID, generation, result.ExpiresAt,
		)
		return err
	})
	return result, err
}

// ProjectActiveOrchestrator returns the id and sandbox provider of a project's
// single active orchestrator, if one exists. A top-level worker is auto-linked
// to it (parent_session_id) and inherits its provider so a project's whole
// worker tree stays on one provider, matching ao spawn'ed children. found is
// false when the project has no live orchestrator, in which case the worker
// stays standalone. Every session has exactly one sandbox row, so the join is
// total; the one-active-orchestrator-per-project unique index makes the match
// unambiguous.
func (s *Store) ProjectActiveOrchestrator(
	ctx context.Context,
	orgID, projectID string,
) (string, string, bool, error) {
	var orchestratorID, provider string
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx,
			`SELECT se.id::text, sb.provider
			FROM ao_sessions se
			JOIN ao_sandboxes sb ON sb.session_id = se.id AND sb.org_id = se.org_id
			WHERE se.org_id = $1 AND se.project_id = $2
			  AND se.kind = 'orchestrator' AND se.is_terminated = false
			  AND se.is_preparation = false
			LIMIT 1`,
			orgID, projectID,
		).Scan(&orchestratorID, &provider)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return orchestratorID, provider, true, nil
}

func (s *Store) CreateGitHubScratchProject(
	ctx context.Context,
	principal domain.Principal,
	orgID, idempotencyKey string,
	maxActiveSandboxes int,
	input domain.CreateGitHubScratchProject,
) (domain.Project, domain.Session, error) {
	var project domain.Project
	var session domain.Session
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		payload, err := json.Marshal(struct {
			RepositoryID            int64                `json:"repositoryId"`
			InstallationID          int64                `json:"installationId"`
			AuthorityUserExternalID string               `json:"authorityUserExternalId"`
			AuthorityEnvironment    string               `json:"authorityEnvironment"`
			CapabilityHash          []byte               `json:"capabilityHash"`
			DisplayName             string               `json:"displayName"`
			Config                  json.RawMessage      `json:"config"`
			Session                 domain.CreateSession `json:"session"`
		}{
			RepositoryID:            input.Repository.GitHubRepositoryID,
			InstallationID:          input.GitHubInstallationID,
			AuthorityUserExternalID: input.AuthorityUserExternalID,
			AuthorityEnvironment:    input.AuthorityEnvironment,
			CapabilityHash:          input.CapabilityHash,
			DisplayName:             input.DisplayName,
			Config:                  input.Config,
			Session:                 input.Session,
		})
		if err != nil {
			return err
		}
		var commandID string
		err = tx.QueryRow(
			ctx,
			`INSERT INTO ao_commands (
				org_id, idempotency_key, kind, payload
			) VALUES ($1, $2, 'github.scratch.create', $3)
			ON CONFLICT (org_id, idempotency_key) DO NOTHING
			RETURNING id`,
			orgID,
			idempotencyKey,
			payload,
		).Scan(&commandID)
		if errors.Is(err, pgx.ErrNoRows) {
			var storedPayload []byte
			var projectID, sessionID, kind, status string
			if err := tx.QueryRow(
				ctx,
				`SELECT kind, status, payload, result->>'projectId',
					result->>'sessionId'
				FROM ao_commands
				WHERE org_id = $1 AND idempotency_key = $2`,
				orgID,
				idempotencyKey,
			).Scan(
				&kind, &status, &storedPayload, &projectID, &sessionID,
			); err != nil {
				return err
			}
			if kind != "github.scratch.create" || status != "succeeded" ||
				!jsonEqual(storedPayload, payload) ||
				projectID == "" || sessionID == "" {
				return ErrIdempotencyMismatch
			}
			if err := scanProject(tx.QueryRow(
				ctx,
				`SELECT id, org_id, display_name, repository_url,
					default_branch, github_repository_id, config,
					created_at, updated_at
				FROM ao_projects
				WHERE org_id = $1 AND id = $2 AND archived_at IS NULL`,
				orgID,
				projectID,
			), &project); err != nil {
				return err
			}
			return getSession(ctx, tx, orgID, sessionID, &session)
		}
		if err != nil {
			return err
		}
		config := input.Config
		if len(config) == 0 {
			config = json.RawMessage(`{"source":"scratch"}`)
		}
		remote := len(input.CapabilityCiphertext) > 0
		if remote {
			if _, err := tx.Exec(
				ctx,
				`INSERT INTO ao_github_repositories (
					github_repository_id, github_owner_account_id, name,
					full_name, html_url, clone_url, ssh_url, default_branch,
					visibility, is_private, is_archived, is_disabled,
					github_updated_at, last_synced_at
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
				input.Repository.GitHubRepositoryID,
				input.Repository.GitHubOwnerID,
				input.Repository.Name,
				input.Repository.FullName,
				input.Repository.HTMLURL,
				input.Repository.CloneURL,
				input.Repository.SSHURL,
				input.Repository.DefaultBranch,
				input.Repository.Visibility,
				input.Repository.IsPrivate,
				input.Repository.IsArchived,
				input.Repository.IsDisabled,
				input.Repository.GitHubUpdatedAt,
			); err != nil {
				return err
			}
			err = scanProject(tx.QueryRow(
				ctx,
				`INSERT INTO ao_projects (
					id, org_id, display_name, repository_url, default_branch, config,
					github_repository_id, github_installation_id,
					github_capability_ciphertext, github_capability_nonce,
					github_authority_user_id, github_authority_environment
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
				RETURNING id, org_id, display_name, repository_url,
					default_branch, github_repository_id, config,
					created_at, updated_at`,
				input.ProjectID,
				orgID,
				input.DisplayName,
				input.Repository.HTMLURL,
				input.Repository.DefaultBranch,
				config,
				input.Repository.GitHubRepositoryID,
				input.GitHubInstallationID,
				input.CapabilityCiphertext,
				input.CapabilityNonce,
				input.AuthorityUserExternalID,
				input.AuthorityEnvironment,
			), &project)
		} else {
			err = scanProject(tx.QueryRow(
				ctx,
				`INSERT INTO ao_projects (
					org_id, display_name, repository_url, default_branch, config,
					github_repository_id, github_repository_grant_id
				)
				SELECT $1, $2, repository.html_url,
					COALESCE(NULLIF(repository.default_branch, ''), 'main'),
					$3, repository.github_repository_id, grant_row.id
				FROM ao_github_repository_grants grant_row
				JOIN ao_github_repositories repository
				  ON repository.github_repository_id = grant_row.github_repository_id
				JOIN ao_github_installations installation
				  ON installation.org_id = grant_row.org_id
				 AND installation.id = grant_row.installation_id
				WHERE grant_row.org_id = $1
				  AND grant_row.github_repository_id = $4
				  AND grant_row.revoked_at IS NULL
				  AND installation.status = 'active'
				RETURNING id, org_id, display_name, repository_url, default_branch,
					github_repository_id, config, created_at, updated_at`,
				orgID,
				input.DisplayName,
				config,
				input.Repository.GitHubRepositoryID,
			), &project)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return normalizeConstraintError(err)
		}
		input.Session.ProjectID = project.ID
		input.Session.Kind = "orchestrator"
		session, err = createSessionTx(
			ctx,
			tx,
			orgID,
			"github-scratch:"+commandID,
			maxActiveSandboxes,
			input.Session,
			"",
			principal.UserID,
		)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_commands
			SET status = 'succeeded',
				result = jsonb_build_object(
					'projectId', $1::text, 'sessionId', $2::text
				),
				updated_at = now()
			WHERE id = $3`,
			project.ID,
			session.ID,
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
				$1, $2, 'github_scratch_project.created', 'project', $3,
				jsonb_build_object('githubRepositoryId', $4::bigint)
			)`,
			orgID,
			principal.UserID,
			project.ID,
			input.Repository.GitHubRepositoryID,
		)
		return err
	})
	return project, session, err
}

const sessionPreparationCompatibilityVersion = 1

func sessionPreparationCompatibilityHash(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	input domain.CreateSession,
	parentSessionID string,
) ([]byte, error) {
	var defaultBranch string
	if err := tx.QueryRow(
		ctx,
		`SELECT default_branch
		FROM ao_projects
		WHERE org_id = $1 AND id = $2 AND archived_at IS NULL`,
		orgID, input.ProjectID,
	).Scan(&defaultBranch); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}

	normalizeJSON := func(value json.RawMessage) (json.RawMessage, error) {
		if len(value) == 0 {
			return json.RawMessage(`{}`), nil
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, err
		}
		return json.Marshal(decoded)
	}
	resourceProfile, err := normalizeJSON(input.ResourceProfile)
	if err != nil {
		return nil, fmt.Errorf("normalize preparation resource profile: %w", err)
	}
	bootstrapContext, err := normalizeJSON(input.BootstrapContext)
	if err != nil {
		return nil, fmt.Errorf("normalize preparation bootstrap context: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider == "" {
		provider = sandbox.DefaultProvider
	}
	document := struct {
		Version            int             `json:"version"`
		ProjectID          string          `json:"projectId"`
		DefaultBranch      string          `json:"defaultBranch"`
		Kind               string          `json:"kind"`
		Harness            string          `json:"harness"`
		Mode               string          `json:"mode"`
		DeniedCommands     []string        `json:"deniedCommands"`
		Provider           string          `json:"provider"`
		ProviderConnection string          `json:"providerConnectionId"`
		ResourceProfile    json.RawMessage `json:"resourceProfile"`
		BootstrapContext   json.RawMessage `json:"bootstrapContext"`
		Release            string          `json:"release"`
		ParentSessionID    string          `json:"parentSessionId"`
	}{
		Version:            sessionPreparationCompatibilityVersion,
		ProjectID:          input.ProjectID,
		DefaultBranch:      defaultBranch,
		Kind:               input.Kind,
		Harness:            input.Harness,
		Mode:               input.Mode,
		DeniedCommands:     input.DeniedCommands,
		Provider:           provider,
		ProviderConnection: input.SandboxConnectionID,
		ResourceProfile:    resourceProfile,
		BootstrapContext:   bootstrapContext,
		Release:            input.Release,
		ParentSessionID:    parentSessionID,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	return hash[:], nil
}

func attachSessionPreparation(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, clientInstanceID string,
	generation int64,
	expiresAt time.Time,
) error {
	_, err := tx.Exec(
		ctx,
		`INSERT INTO ao_preparation_attachments (
			org_id, session_id, client_instance_id, generation, lease_expires_at
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, client_instance_id) DO UPDATE
		SET generation = EXCLUDED.generation,
			attached_at = now(),
			last_activity_at = now(),
			lease_expires_at = EXCLUDED.lease_expires_at,
			detached_at = NULL`,
		orgID, sessionID, clientInstanceID, generation, expiresAt,
	)
	return err
}

func expireSessionPreparation(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID string,
) error {
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_sandboxes
		SET desired_state = 'deleted',
			preparation_generation = preparation_generation + 1,
			reconcile_after = now(),
			reconcile_lease_owner = '',
			reconcile_lease_until = NULL,
			updated_at = now()
		WHERE org_id = $1 AND session_id = $2
		  AND desired_state <> 'deleted'`,
		orgID, sessionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_sessions
		SET is_terminated = true,
			activity_state = 'exited',
			updated_at = now()
		WHERE org_id = $1 AND id = $2`,
		orgID, sessionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_access_tickets
		SET consumed_at = COALESCE(consumed_at, now())
		WHERE org_id = $1 AND session_id = $2 AND consumed_at IS NULL`,
		orgID, sessionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_worker_connections
		SET disconnected_at = COALESCE(disconnected_at, now())
		WHERE org_id = $1 AND session_id = $2 AND disconnected_at IS NULL`,
		orgID, sessionID,
	); err != nil {
		return err
	}
	return notifySandboxReconcile(ctx, tx)
}

func createSessionTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, idempotencyKey string,
	maxActiveSandboxes int,
	input domain.CreateSession,
	parentSessionID, actorUserID string,
) (domain.Session, error) {
	if input.Mode == "" {
		input.Mode = "standard"
	}
	if input.DeniedCommands == nil {
		input.DeniedCommands = []string{}
	}
	isPreparation := input.PreparationExpiresAfter > 0
	if isPreparation && strings.TrimSpace(input.PreparationClientInstanceID) == "" {
		return domain.Session{}, ErrInvalid
	}
	var session domain.Session

	// Serialize quota allocation before inserting any rows that reference the
	// organization. Taking this lock later can deadlock concurrent creators
	// through their foreign-key key-share locks.
	var lockedOrgID string
	if err := tx.QueryRow(
		ctx,
		`SELECT id FROM ao_organizations WHERE id = $1 FOR UPDATE`,
		orgID,
	).Scan(&lockedOrgID); err != nil {
		return domain.Session{}, err
	}

	payload, err := json.Marshal(input)
	commandKind := "session.create"
	if parentSessionID != "" {
		commandKind = "session.child.create"
		payload, err = json.Marshal(struct {
			Input           domain.CreateSession `json:"input"`
			ParentSessionID string               `json:"parentSessionId"`
		}{Input: input, ParentSessionID: parentSessionID})
	}
	if err != nil {
		return domain.Session{}, err
	}
	var commandID string
	err = tx.QueryRow(
		ctx,
		`INSERT INTO ao_commands (
			org_id, idempotency_key, kind, payload
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, idempotency_key) DO NOTHING
		RETURNING id`,
		orgID,
		idempotencyKey,
		commandKind,
		payload,
	).Scan(&commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = loadIdempotentSession(
			ctx,
			tx,
			orgID,
			idempotencyKey,
			payload,
			commandKind,
			&session,
		)
		return session, err
	}
	if err != nil {
		return domain.Session{}, err
	}

	var compatibilityHash []byte
	if isPreparation {
		compatibilityHash, err = sessionPreparationCompatibilityHash(
			ctx, tx, orgID, input, parentSessionID,
		)
		if err != nil {
			return domain.Session{}, err
		}

		var existingSessionID, desiredState string
		var expired bool
		var generation int64
		err = tx.QueryRow(
			ctx,
			`SELECT session.id::text,
				COALESCE(
					session.preparation_expires_at <= clock_timestamp()
					OR sandbox.preparation_expires_at <= clock_timestamp(),
					true
				),
				sandbox.desired_state,
				sandbox.preparation_generation
			FROM ao_sessions session
			JOIN ao_sandboxes sandbox
			  ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
			WHERE session.org_id = $1
			  AND session.created_by_user_id = $2
			  AND session.preparation_compatibility_hash = $3
			  AND session.is_preparation = true
			  AND session.is_terminated = false
			FOR UPDATE OF session, sandbox`,
			orgID, actorUserID, compatibilityHash,
		).Scan(&existingSessionID, &expired, &desiredState, &generation)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, err
		}
		if err == nil && (expired || desiredState != domain.SandboxDesiredRunning) {
			if err := expireSessionPreparation(ctx, tx, orgID, existingSessionID); err != nil {
				return domain.Session{}, err
			}
			existingSessionID = ""
		}
		if existingSessionID != "" {
			var expiresAt time.Time
			if err := tx.QueryRow(
				ctx,
				`SELECT clock_timestamp() + $1::interval`,
				intervalString(input.PreparationExpiresAfter),
			).Scan(&expiresAt); err != nil {
				return domain.Session{}, err
			}
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_sessions
				SET preparation_expires_at = $3, updated_at = now()
				WHERE org_id = $1 AND id = $2`,
				orgID, existingSessionID, expiresAt,
			); err != nil {
				return domain.Session{}, err
			}
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_sandboxes
				SET preparation_expires_at = $3, updated_at = now()
				WHERE org_id = $1 AND session_id = $2`,
				orgID, existingSessionID, expiresAt,
			); err != nil {
				return domain.Session{}, err
			}
			if err := attachSessionPreparation(
				ctx, tx, orgID, existingSessionID,
				input.PreparationClientInstanceID, generation,
				expiresAt,
			); err != nil {
				return domain.Session{}, err
			}
			if _, err := tx.Exec(
				ctx,
				`UPDATE ao_commands
				SET session_id = $1, status = 'succeeded',
					result = jsonb_build_object(
						'sessionId', $2::text,
						'disposition', 'reused',
						'generation', $3::bigint
					),
					updated_at = now()
				WHERE id = $4`,
				existingSessionID, existingSessionID, generation, commandID,
			); err != nil {
				return domain.Session{}, err
			}
			if err := getSession(ctx, tx, orgID, existingSessionID, &session); err != nil {
				return domain.Session{}, err
			}
			return session, nil
		}
	}

	var activeSandboxes int
	if err := tx.QueryRow(
		ctx,
		`SELECT count(*) FROM ao_sandboxes
		WHERE org_id = $1
			AND desired_state <> 'deleted'
			AND observed_state NOT IN ('deleted', 'deleting', 'terminated', 'failed')`,
		orgID,
	).Scan(&activeSandboxes); err != nil {
		return domain.Session{}, err
	}
	if maxActiveSandboxes < 1 || activeSandboxes >= maxActiveSandboxes {
		return domain.Session{}, ErrSandboxQuotaExceeded
	}

	err = scanSession(tx.QueryRow(
		ctx,
		`WITH generated AS (SELECT gen_random_uuid() AS id)
		INSERT INTO ao_sessions (
			id, org_id, project_id, kind, harness, display_name, branch,
			prompt, mode, denied_commands, parent_session_id, created_by_user_id,
			is_preparation, preparation_expires_at,
			preparation_compatibility_version, preparation_compatibility_hash
		)
		SELECT id, $1, $2, $3, $4, $5, 'ao/' || left(id::text, 8),
			$6, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, '')::uuid,
			$11, CASE WHEN $11 THEN now() + $12::interval ELSE NULL END,
			CASE WHEN $11 THEN $13::smallint ELSE NULL END,
			CASE WHEN $11 THEN $14::bytea ELSE NULL END
		FROM generated
		RETURNING id, org_id, project_id, kind, harness, display_name, branch,
			mode, denied_commands, activity_state, is_terminated,
			false, '', '', '', '', '', 0, preparation_expires_at, created_at, updated_at`,
		orgID,
		input.ProjectID,
		input.Kind,
		input.Harness,
		input.DisplayName,
		input.Prompt,
		input.Mode,
		input.DeniedCommands,
		parentSessionID,
		actorUserID,
		isPreparation,
		intervalString(input.PreparationExpiresAfter),
		sessionPreparationCompatibilityVersion,
		compatibilityHash,
	), &session)
	if err != nil {
		return domain.Session{}, normalizeConstraintError(err)
	}

	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider == "" {
		provider = sandbox.DefaultProvider
	}
	if input.SandboxConnectionID != "" {
		var connectionProvider string
		if err := tx.QueryRow(
			ctx,
			`SELECT provider FROM ao_provider_connections
			WHERE org_id = $1 AND id = $2`,
			orgID,
			input.SandboxConnectionID,
		).Scan(&connectionProvider); errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, ErrNotFound
		} else if err != nil {
			return domain.Session{}, err
		}
		if connectionProvider != provider {
			return domain.Session{}, ErrInvalid
		}
	}
	resourceProfile, err := patchSandboxJSON(
		input.ResourceProfile, provider, orgID, session.ID,
		input.Release, input.SandboxConnectionID, false,
	)
	if err != nil {
		return domain.Session{}, err
	}
	bootstrapContext, err := patchSandboxJSON(
		input.BootstrapContext, provider, orgID, session.ID,
		input.Release, input.SandboxConnectionID, true,
	)
	if err != nil {
		return domain.Session{}, err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_sandboxes (
			session_id, org_id, provider, provider_connection_id,
			resource_profile, bootstrap_context, preparation_expires_at,
			preparation_generation
		) VALUES (
			$1, $2, $3, NULLIF($4, '')::uuid, $5, $6,
			CASE WHEN $7 THEN now() + $8::interval ELSE NULL END,
			CASE WHEN $7 THEN 1 ELSE 0 END
		)`,
		session.ID, orgID, provider, input.SandboxConnectionID,
		resourceProfile, bootstrapContext, isPreparation,
		intervalString(input.PreparationExpiresAfter),
	); err != nil {
		return domain.Session{}, normalizeConstraintError(err)
	}
	if isPreparation {
		if err := attachSessionPreparation(
			ctx, tx, orgID, session.ID,
			input.PreparationClientInstanceID, 1,
			*session.PreparationExpiresAt,
		); err != nil {
			return domain.Session{}, err
		}
	}
	if input.Prompt != "" {
		if _, err := appendUserMessageEvent(
			ctx, tx, orgID, session.ID, input.Prompt, 0,
		); err != nil {
			return domain.Session{}, err
		}
	}

	commandResult := `jsonb_build_object('sessionId', $2::text)`
	if isPreparation {
		commandResult = `jsonb_build_object(
			'sessionId', $2::text,
			'disposition', 'created',
			'generation', 1
		)`
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_commands
		SET session_id = $1, status = 'succeeded',
			result = `+commandResult+`,
			updated_at = now()
		WHERE id = $3`,
		session.ID, session.ID, commandID,
	); err != nil {
		return domain.Session{}, err
	}
	auditSQL := `INSERT INTO ao_audit_events (
		org_id, actor_user_id, action, resource_type, resource_id
	) VALUES ($1, $2, 'session.created', 'session', $3)`
	auditArgs := []any{orgID, actorUserID, session.ID}
	if parentSessionID != "" {
		auditSQL = `INSERT INTO ao_audit_events (
			org_id, action, resource_type, resource_id, metadata
		) VALUES (
			$1, 'session.created', 'session', $2,
			jsonb_build_object('parentSessionId', $3::text)
		)`
		auditArgs = []any{orgID, session.ID, parentSessionID}
	}
	if _, err = tx.Exec(ctx, auditSQL, auditArgs...); err != nil {
		return domain.Session{}, err
	}
	if err := getSession(ctx, tx, orgID, session.ID, &session); err != nil {
		return domain.Session{}, err
	}
	if err := notifySandboxReconcile(ctx, tx); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func loadIdempotentSession(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	idempotencyKey string,
	payload []byte,
	expectedKind string,
	session *domain.Session,
) error {
	var storedPayload []byte
	var sessionID string
	var kind, status string
	err := tx.QueryRow(
		ctx,
		`SELECT kind, status, payload, result->>'sessionId'
		FROM ao_commands
		WHERE org_id = $1 AND idempotency_key = $2`,
		orgID,
		idempotencyKey,
	).Scan(&kind, &status, &storedPayload, &sessionID)
	if err != nil {
		return err
	}
	if kind != expectedKind || status != "succeeded" ||
		!jsonEqual(storedPayload, payload) || sessionID == "" {
		return ErrIdempotencyMismatch
	}
	return getSession(ctx, tx, orgID, sessionID, session)
}

func (s *Store) ListSessions(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	projectID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.Session, bool, error) {
	var sessions []domain.Session
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			sessionSelect+`
			WHERE session.org_id = $1
			  AND session.is_preparation = false
			  AND EXISTS (
				SELECT 1 FROM ao_projects project
				WHERE project.org_id = session.org_id
				  AND project.id = session.project_id
				  AND project.archived_at IS NULL
			  )
			  AND ($2 = '' OR session.project_id = $2::uuid)
			  AND ($3::timestamptz IS NULL OR (session.updated_at, session.id) < ($3, $4::uuid))
			ORDER BY session.updated_at DESC, session.id DESC
			LIMIT $5`,
			orgID,
			projectID,
			cursorTime(cursor),
			cursorID(cursor),
			limit+1,
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

// ListSessionChildren lists the sessions an orchestrator spawned, for the
// human-facing Workers view. Unlike the worker-auth ListOrchestratorChildren
// it includes terminated children (history is the point of the view) and does
// not require the parent to still be alive or an orchestrator — a
// non-orchestrator or unknown parent simply owns no children, which returns an
// empty page rather than an error.
func (s *Store) ListSessionChildren(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	parentSessionID string,
	cursor *domain.Cursor,
	limit int,
) ([]domain.Session, bool, error) {
	var sessions []domain.Session
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			sessionSelect+`
			WHERE session.org_id = $1
			  AND session.parent_session_id = $2::uuid
			  AND session.is_preparation = false
			  AND ($3::timestamptz IS NULL OR (session.updated_at, session.id) < ($3, $4::uuid))
			ORDER BY session.updated_at DESC, session.id DESC
			LIMIT $5`,
			orgID,
			parentSessionID,
			cursorTime(cursor),
			cursorID(cursor),
			limit+1,
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

func (s *Store) GetSession(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	sessionID string,
) (domain.Session, error) {
	var session domain.Session
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		return getSession(ctx, tx, orgID, sessionID, &session)
	})
	return session, err
}

const sessionSelect = `
	SELECT session.id, session.org_id, session.project_id, session.kind,
		session.harness, session.display_name, session.branch,
		session.mode, session.denied_commands,
		CASE
			WHEN EXISTS (
				SELECT 1 FROM ao_turns turn
				WHERE turn.org_id = session.org_id AND turn.session_id = session.id
					AND turn.state IN ('queued', 'claimed', 'running')
			) THEN 'active'
			ELSE session.activity_state
		END AS activity_state,
		session.is_terminated,
		EXISTS (
			SELECT 1 FROM ao_worker_connections worker
			WHERE worker.session_id = session.id AND worker.disconnected_at IS NULL
		),
		COALESCE(sandbox.provider, ''),
		COALESCE(sandbox.desired_state, ''),
		COALESCE(sandbox.observed_state, ''),
		COALESCE(sandbox.observed_state, ''),
		COALESCE(sandbox.last_error, ''),
		COALESCE((
			SELECT MAX(terminal.worker_epoch)
			FROM ao_terminal_sessions terminal
			WHERE terminal.org_id = session.org_id
				AND terminal.session_id = session.id
				AND terminal.kind = 'agent'
		), 0),
		session.preparation_expires_at,
		session.created_at, session.updated_at
	FROM ao_sessions session
	LEFT JOIN ao_sandboxes sandbox
		ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
`

func getSession(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	sessionID string,
	session *domain.Session,
) error {
	err := scanSession(tx.QueryRow(
		ctx,
		sessionSelect+` WHERE session.org_id = $1 AND session.id = $2`,
		orgID,
		sessionID,
	), session)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(row scanner, project *domain.Project) error {
	return row.Scan(
		&project.ID,
		&project.OrgID,
		&project.DisplayName,
		&project.RepositoryURL,
		&project.DefaultBranch,
		&project.GitHubRepositoryID,
		&project.Config,
		&project.CreatedAt,
		&project.UpdatedAt,
	)
}

func scanSession(row scanner, session *domain.Session) error {
	var activity string
	err := row.Scan(
		&session.ID,
		&session.OrgID,
		&session.ProjectID,
		&session.Kind,
		&session.Harness,
		&session.DisplayName,
		&session.Branch,
		&session.Mode,
		&session.DeniedCommands,
		&activity,
		&session.IsTerminated,
		&session.RuntimeConnected,
		&session.SandboxProvider,
		&session.DesiredState,
		&session.ObservedState,
		&session.RuntimeState,
		&session.RuntimeError,
		&session.WorkerEpoch,
		&session.PreparationExpiresAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	)
	session.ActivityState = contract.ActivityState(activity)
	return err
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(left, right)
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return bytes.Equal(leftJSON, rightJSON)
}

func patchSandboxJSON(
	raw json.RawMessage,
	provider, orgID, sessionID, release, providerConnectionID string,
	isBootstrap bool,
) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, ErrInvalid
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	object["provider"] = provider
	object["orgId"] = orgID
	object["sessionId"] = sessionID
	if release != "" {
		object["release"] = release
	}
	if providerConnectionID != "" {
		object["providerConnectionId"] = providerConnectionID
	}
	if isBootstrap {
		object["kind"] = "bootstrap"
	} else {
		object["kind"] = "resource-profile"
	}
	patched, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return patched, nil
}

func cursorTime(cursor *domain.Cursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.Time
}

func cursorID(cursor *domain.Cursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.ID
}
