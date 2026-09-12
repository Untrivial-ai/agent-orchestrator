package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

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
			ctx, tx, orgID, idempotencyKey, maxActiveSandboxes, input, "", principal.UserID,
		)
		return err
	})
	return session, err
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

	var activeSandboxes int
	if err := tx.QueryRow(
		ctx,
		`SELECT count(*) FROM ao_sandboxes
		WHERE org_id = $1
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
			prompt, mode, denied_commands, parent_session_id, created_by_user_id
		)
		SELECT id, $1, $2, $3, $4, $5, 'ao/' || left(id::text, 8),
			$6, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, '')::uuid
		FROM generated
		RETURNING id, org_id, project_id, kind, harness, display_name, branch,
			mode, denied_commands, activity_state, is_terminated,
			false, '', '', created_at, updated_at`,
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
			resource_profile, bootstrap_context
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6)`,
		session.ID, orgID, provider, input.SandboxConnectionID,
		resourceProfile, bootstrapContext,
	); err != nil {
		return domain.Session{}, normalizeConstraintError(err)
	}
	if input.Prompt != "" {
		if _, err := appendUserMessageEvent(
			ctx, tx, orgID, session.ID, input.Prompt,
		); err != nil {
			return domain.Session{}, err
		}
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_commands
		SET session_id = $1, status = 'succeeded',
			result = jsonb_build_object('sessionId', $2::text),
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
		COALESCE(sandbox.observed_state, ''),
		COALESCE(sandbox.last_error, ''),
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
		&session.RuntimeState,
		&session.RuntimeError,
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
