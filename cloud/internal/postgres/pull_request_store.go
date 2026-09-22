package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

const pullRequestColumns = `id, org_id, session_id, provider, repository, author, author_avatar_url, number, url, title,
	state, draft, head_sha, base_sha, merge_commit_sha, source_branch, target_branch, additions, deletions, changed_files,
	ci_state, review_state, mergeability, checks, claimed_by_session_id, claimed_at, released_at,
	ao_review_state, review_partial, created_at_provider, updated_at_provider, merged_at_provider, closed_at_provider,
	observed_at, created_at, updated_at`

// CreatePullRequestRecord persists a pull request already created on GitHub.
func (s *Store) CreatePullRequestRecord(
	ctx context.Context,
	orgID, sessionID string,
	provider, repository, author string,
	number int,
	url, sourceBranch, targetBranch, headSHA, title string,
	additions, deletions, changedFiles int,
) (domain.PullRequest, error) {
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		record, err = scanPullRequest(tx.QueryRow(
			ctx,
			`INSERT INTO ao_pull_requests (
				org_id, session_id, provider, repository, author, number, url, title,
				state, head_sha, source_branch, target_branch, additions, deletions, changed_files,
				claimed_by_session_id, claimed_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $2, now())
			RETURNING `+pullRequestColumns,
			orgID, sessionID, provider, repository, author, number, url, title,
			string(contract.PRStateOpen), headSHA, sourceBranch, targetBranch,
			additions, deletions, changedFiles,
		))
		if err != nil {
			return err
		}
		// PR creation changes both the worker's activity projection and the
		// project's aggregate inspector. The session timestamp is the durable
		// invalidation key observed by the UI's session stream.
		_, err = tx.Exec(ctx,
			`UPDATE ao_sessions SET updated_at = now() WHERE org_id = $1 AND id = $2`,
			orgID, sessionID,
		)
		return err
	})
	if err != nil {
		return domain.PullRequest{}, normalizeConstraintError(err)
	}
	return record, nil
}

// ClaimPullRequestRecord adopts an existing provider pull request for the
// worker that opened it. Repeating the call is safe: this is the durable
// boundary between worker-side GitHub tooling and AO's inspector projections.
func (s *Store) ClaimPullRequestRecord(
	ctx context.Context,
	orgID, sessionID string,
	input domain.PullRequest,
) (domain.PullRequest, error) {
	if strings.TrimSpace(input.Provider) != "github" || strings.TrimSpace(input.Repository) == "" ||
		input.Number <= 0 || strings.TrimSpace(input.URL) == "" || strings.TrimSpace(input.Title) == "" ||
		strings.TrimSpace(input.HeadSHA) == "" || strings.TrimSpace(input.SourceBranch) == "" ||
		strings.TrimSpace(input.TargetBranch) == "" {
		return domain.PullRequest{}, ErrInvalid
	}
	state := input.State
	if state == contract.PRStateDraft {
		state = contract.PRStateOpen
		input.Draft = true
	}
	if state != contract.PRStateOpen && state != contract.PRStateClosed && state != contract.PRStateMerged {
		return domain.PullRequest{}, ErrInvalid
	}
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		record, err = scanPullRequest(tx.QueryRow(
			ctx,
			`INSERT INTO ao_pull_requests (
				org_id, session_id, provider, repository, author, number, url, title,
				state, draft, head_sha, source_branch, target_branch, additions, deletions, changed_files,
				claimed_by_session_id, claimed_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $2, now())
			ON CONFLICT (org_id, provider, repository, number) DO UPDATE
			SET session_id = EXCLUDED.session_id,
				author = EXCLUDED.author,
				url = EXCLUDED.url,
				title = EXCLUDED.title,
				state = EXCLUDED.state,
				draft = EXCLUDED.draft,
				head_sha = EXCLUDED.head_sha,
				source_branch = EXCLUDED.source_branch,
				target_branch = EXCLUDED.target_branch,
				additions = EXCLUDED.additions,
				deletions = EXCLUDED.deletions,
				changed_files = EXCLUDED.changed_files,
				claimed_by_session_id = EXCLUDED.claimed_by_session_id,
				claimed_at = now(),
				released_at = NULL,
				observed_at = now(),
				updated_at = now()
			RETURNING `+pullRequestColumns,
			orgID, sessionID, input.Provider, input.Repository, input.Author, input.Number, input.URL, input.Title,
			string(state), input.Draft, input.HeadSHA, input.SourceBranch, input.TargetBranch,
			input.Additions, input.Deletions, input.ChangedFiles,
		))
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_sessions SET updated_at = now() WHERE org_id = $1 AND id = $2`,
			orgID, sessionID,
		)
		return err
	})
	if err != nil {
		return domain.PullRequest{}, normalizeConstraintError(err)
	}
	return record, nil
}

// GetPullRequest returns one pull request by its durable ID.
func (s *Store) GetPullRequest(
	ctx context.Context,
	orgID, pullRequestID string,
) (domain.PullRequest, error) {
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		record, err = scanPullRequest(tx.QueryRow(
			ctx,
			`SELECT `+pullRequestColumns+`
			FROM ao_pull_requests
			WHERE org_id = $1 AND id = $2`,
			orgID, pullRequestID,
		))
		return err
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	return record, nil
}

// ListPullRequestsBySession returns a session's pull requests, newest first.
func (s *Store) ListPullRequestsBySession(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) ([]domain.PullRequest, error) {
	var records []domain.PullRequest
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		rows, err := tx.Query(
			ctx,
			`SELECT `+pullRequestColumns+`
			FROM ao_pull_requests pr
			WHERE pr.org_id = $1
				AND (
					pr.session_id = $2
					OR pr.claimed_by_session_id = $2
					OR EXISTS (
						SELECT 1
						FROM ao_sessions requested
						JOIN ao_sessions owner
							ON owner.org_id = pr.org_id AND owner.id = pr.session_id
						WHERE requested.org_id = $1 AND requested.id = $2
							AND requested.kind = 'orchestrator'
							AND owner.project_id = requested.project_id
					)
				)
			ORDER BY pr.created_at DESC`,
			orgID, sessionID,
		)
		if err != nil {
			return fmt.Errorf("list pull requests: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			record, err := scanPullRequest(rows)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// PRFactsBySession returns pull request facts grouped by session ID.
func (s *Store) PRFactsBySession(
	ctx context.Context,
	orgID string,
	sessionIDs []string,
) (map[string][]contract.PRFacts, error) {
	facts := make(map[string][]contract.PRFacts)
	if len(sessionIDs) == 0 {
		return facts, nil
	}
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT pr.session_id, pr.url, pr.state, pr.draft, pr.source_branch, pr.target_branch,
				pr.ci_state, pr.review_state, pr.mergeability,
				EXISTS (
					SELECT 1
					FROM ao_pr_review_comments comment
					WHERE comment.org_id = pr.org_id
						AND comment.pull_request_id = pr.id
						AND comment.is_resolved = false
						AND comment.is_outdated = false
						AND comment.is_bot = false
						AND comment.path <> ''
						AND comment.line IS NOT NULL
				) AS review_comments
			FROM ao_pull_requests pr
			WHERE pr.org_id = $1 AND pr.session_id = ANY($2)`,
			orgID, sessionIDs,
		)
		if err != nil {
			return fmt.Errorf("list pull request facts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var sessionID, url, state, sourceBranch, targetBranch string
			var ciState, reviewState, mergeability string
			var draft, reviewComments bool
			if err := rows.Scan(
				&sessionID, &url, &state, &draft, &sourceBranch, &targetBranch,
				&ciState, &reviewState, &mergeability, &reviewComments,
			); err != nil {
				return fmt.Errorf("scan pull request facts: %w", err)
			}
			prState := contract.PRState(state)
			facts[sessionID] = append(facts[sessionID], contract.PRFacts{
				URL:            url,
				Draft:          draft,
				Merged:         prState == contract.PRStateMerged,
				Closed:         prState == contract.PRStateClosed,
				CI:             contract.CIState(ciState),
				Review:         contract.ReviewDecision(reviewState),
				Mergeability:   contract.Mergeability(mergeability),
				ReviewComments: reviewComments,
				SourceBranch:   sourceBranch,
				TargetBranch:   targetBranch,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
}

// PullRequestsBySessions returns full pull request rows grouped by session ID.
// Unlike PRFactsBySession (which feeds status derivation via the shared
// contract facts), this carries number, url, and timestamps for wire responses
// that render the PRs themselves.
func (s *Store) PullRequestsBySessions(
	ctx context.Context,
	orgID string,
	sessionIDs []string,
) (map[string][]domain.PullRequest, error) {
	records := make(map[string][]domain.PullRequest)
	if len(sessionIDs) == 0 {
		return records, nil
	}
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT `+pullRequestColumns+`
			FROM ao_pull_requests
			WHERE org_id = $1 AND session_id = ANY($2)
			ORDER BY updated_at DESC`,
			orgID, sessionIDs,
		)
		if err != nil {
			return fmt.Errorf("list pull requests by session: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			record, err := scanPullRequest(rows)
			if err != nil {
				return fmt.Errorf("scan pull request: %w", err)
			}
			records[record.SessionID] = append(records[record.SessionID], record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// OpenPullRequestRefs lists open pull requests across organizations.
func (s *Store) OpenPullRequestRefs(ctx context.Context) ([]domain.PullRequestRef, error) {
	var refs []domain.PullRequestRef
	err := s.withService(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT id, org_id, provider, repository, number
			FROM ao_pull_requests
			WHERE state = 'open'`,
		)
		if err != nil {
			return fmt.Errorf("list open pull requests: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ref domain.PullRequestRef
			if err := rows.Scan(&ref.ID, &ref.OrgID, &ref.Provider, &ref.Repository, &ref.Number); err != nil {
				return err
			}
			refs = append(refs, ref)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// UpdatePullRequestObservation applies a freshly fetched GitHub snapshot over
// a pull request's durable record.
func (s *Store) UpdatePullRequestObservation(
	ctx context.Context,
	orgID, pullRequestID string,
	observation domain.PullRequestObservation,
) (domain.PullRequest, error) {
	state := observation.State
	if state == contract.PRStateDraft {
		state = contract.PRStateOpen
	}
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		previous, err := scanPullRequest(tx.QueryRow(ctx,
			`SELECT `+pullRequestColumns+` FROM ao_pull_requests
			WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, pullRequestID))
		if err != nil {
			return err
		}
		record, err = scanPullRequest(tx.QueryRow(
			ctx,
			`UPDATE ao_pull_requests
			SET state = $3, draft = $4, head_sha = $5, additions = $6, deletions = $7,
				changed_files = $8, ci_state = $9, review_state = $10, mergeability = $11,
				checks = $12,
				observed_at = now(), updated_at = now()
			WHERE org_id = $1 AND id = $2
			RETURNING `+pullRequestColumns,
			orgID, pullRequestID,
			string(state), observation.Draft, observation.HeadSHA,
			observation.Additions, observation.Deletions, observation.ChangedFiles,
			string(observation.CIState), string(observation.ReviewState), string(observation.Mergeability),
			observation.Checks,
		))
		if err != nil {
			return err
		}
		_, err = recordPullRequestTransitionTx(ctx, tx, previous, record)
		return err
	})
	if err != nil {
		return domain.PullRequest{}, normalizeConstraintError(err)
	}
	return record, nil
}

// PullRequestByGitHubReference resolves a tracked PR through the repository
// bound to its project. orgID is derived from the installation route, never
// from webhook payload data.
func (s *Store) PullRequestByGitHubReference(
	ctx context.Context,
	orgID string,
	repositoryID int64,
	number int,
) (domain.PullRequest, error) {
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		record, err = scanPullRequest(tx.QueryRow(ctx,
			`SELECT `+pullRequestColumns+`
			FROM ao_pull_requests pull_request
			WHERE org_id = $1 AND number = $3 AND EXISTS (
				SELECT 1 FROM ao_github_repositories repository
				WHERE repository.github_repository_id = $2
				  AND lower(repository.full_name) = lower(pull_request.repository)
			)
			ORDER BY updated_at DESC LIMIT 1`,
			orgID, repositoryID, number,
		))
		return err
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	return record, nil
}

// PullRequestByGitHubHead resolves check events whose GitHub payload omits the
// pull_requests association but still carries the checked commit SHA.
func (s *Store) PullRequestByGitHubHead(
	ctx context.Context,
	orgID string,
	repositoryID int64,
	headSHA string,
) (domain.PullRequest, error) {
	var record domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		record, err = scanPullRequest(tx.QueryRow(ctx,
			`SELECT `+pullRequestColumns+`
			FROM ao_pull_requests pull_request
			WHERE org_id = $1 AND head_sha = $3 AND EXISTS (
				SELECT 1 FROM ao_github_repositories repository
				WHERE repository.github_repository_id = $2
				  AND lower(repository.full_name) = lower(pull_request.repository)
			)
			ORDER BY updated_at DESC LIMIT 1`,
			orgID, repositoryID, headSHA,
		))
		return err
	})
	if err != nil {
		return domain.PullRequest{}, err
	}
	return record, nil
}

// PullRequestsByGitHubRepository returns tracked open PRs for repository-wide
// invalidations such as base-branch pushes. Repository identity is resolved
// inside the installation-routed organization boundary.
func (s *Store) PullRequestsByGitHubRepository(
	ctx context.Context,
	orgID string,
	repositoryID int64,
) ([]domain.PullRequest, error) {
	var records []domain.PullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pullRequestColumns+`
			FROM ao_pull_requests pull_request
			WHERE org_id = $1 AND state = 'open' AND EXISTS (
				SELECT 1 FROM ao_github_repositories repository
				WHERE repository.github_repository_id = $2
				  AND lower(repository.full_name) = lower(pull_request.repository)
			)
			ORDER BY updated_at DESC`, orgID, repositoryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			record, err := scanPullRequest(rows)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		return rows.Err()
	})
	return records, err
}

// RecordPullRequestOpened creates or refreshes the durable bell notification
// for an AO-tracked pull request opened through the installed GitHub App.
func (s *Store) RecordPullRequestOpened(
	ctx context.Context,
	orgID string,
	pr domain.PullRequest,
	deliveryID string,
) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var projectID, recipientID string
		if err := tx.QueryRow(ctx, `
			SELECT project_id::text, COALESCE(created_by_user_id::text, '')
			FROM ao_sessions WHERE org_id = $1 AND id = $2`,
			orgID, pr.SessionID).Scan(&projectID, &recipientID); err != nil {
			return err
		}
		if recipientID == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true)`, recipientID); err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]any{
			"pullRequestId": pr.ID, "pullRequestUrl": pr.URL,
			"pullRequestNumber": pr.Number, "repository": pr.Repository,
		})
		if err != nil {
			return err
		}
		dedupeKey := "pr-opened:" + pr.ID
		var notificationID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO ao_notifications (
				org_id, recipient_user_id, project_id, session_id, pull_request_id,
				source, type, title, body, metadata, dedupe_key, source_event_id, status
			) VALUES ($1, $2, $3, $4, $5, 'cloud', 'pr_opened',
				'Pull request opened', $6, $7, $8, $9, 'unread')
			ON CONFLICT (org_id, recipient_user_id, dedupe_key)
				WHERE resolved_at IS NULL
			DO UPDATE SET body = EXCLUDED.body, metadata = EXCLUDED.metadata,
				source_event_id = EXCLUDED.source_event_id, status = 'unread', updated_at = now()
			RETURNING id::text`,
			orgID, recipientID, projectID, pr.SessionID, pr.ID,
			fmt.Sprintf("%s#%d is ready for review.", pr.Repository, pr.Number),
			metadata, dedupeKey, deliveryID).Scan(&notificationID); err != nil {
			return err
		}
		var snapshot []byte
		if err := tx.QueryRow(ctx, `
			SELECT jsonb_build_object(
				'id', id::text, 'orgId', org_id::text,
				'recipientUserId', recipient_user_id::text,
				'projectId', project_id::text, 'sessionId', session_id::text,
				'source', source, 'type', type, 'title', title, 'body', body,
				'status', status, 'eventId', source_event_id, 'metadata', metadata,
				'createdAt', created_at, 'updatedAt', updated_at
			) FROM ao_notifications
			WHERE org_id = $1 AND id = $2`, orgID, notificationID).Scan(&snapshot); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ao_notification_events (
				org_id, recipient_user_id, notification_id, kind, source_event_id, snapshot
			) VALUES ($1, $2, $3, 'notification_created', $4, $5)`,
			orgID, recipientID, notificationID, deliveryID, snapshot); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SELECT pg_notify('ao_notification_event', $1)`, orgID)
		return err
	})
}

type pullRequestRow interface {
	Scan(dest ...any) error
}

func scanPullRequest(row pullRequestRow) (domain.PullRequest, error) {
	var record domain.PullRequest
	var state, reviewState, mergeability, ciState, aoReviewState string
	err := row.Scan(
		&record.ID, &record.OrgID, &record.SessionID, &record.Provider, &record.Repository,
		&record.Author, &record.AuthorAvatarURL, &record.Number, &record.URL, &record.Title, &state, &record.Draft, &record.HeadSHA,
		&record.BaseSHA, &record.MergeCommitSHA, &record.SourceBranch, &record.TargetBranch, &record.Additions, &record.Deletions, &record.ChangedFiles,
		&ciState, &reviewState, &mergeability,
		&record.Checks, &record.ClaimedBySessionID, &record.ClaimedAt, &record.ReleasedAt,
		&aoReviewState, &record.ReviewPartial, &record.CreatedAtProvider, &record.UpdatedAtProvider,
		&record.MergedAtProvider, &record.ClosedAtProvider, &record.ObservedAt, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PullRequest{}, ErrNotFound
	}
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("scan pull request: %w", err)
	}
	record.State = contract.PRState(state)
	if record.Draft && record.State == contract.PRStateOpen {
		record.State = contract.PRStateDraft
	}
	record.CIState = contract.CIState(ciState)
	record.ReviewState = contract.ReviewDecision(reviewState)
	record.Mergeability = contract.Mergeability(mergeability)
	record.AOReviewState = contract.AOReviewState(aoReviewState)
	return record, nil
}
