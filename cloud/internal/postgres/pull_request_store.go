package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

const pullRequestColumns = `id, org_id, session_id, provider, repository, author, number, url, title,
	state, draft, head_sha, source_branch, target_branch, additions, deletions, changed_files,
	ci_state, review_state, mergeability, checks, claimed_by_session_id, claimed_at, released_at,
	ao_review_state, observed_at, created_at, updated_at`

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
			`SELECT session_id, url, state, draft, source_branch, target_branch,
				ci_state, review_state, mergeability
			FROM ao_pull_requests
			WHERE org_id = $1 AND session_id = ANY($2)`,
			orgID, sessionIDs,
		)
		if err != nil {
			return fmt.Errorf("list pull request facts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var sessionID, url, state, sourceBranch, targetBranch string
			var ciState, reviewState, mergeability string
			var draft bool
			if err := rows.Scan(
				&sessionID, &url, &state, &draft, &sourceBranch, &targetBranch,
				&ciState, &reviewState, &mergeability,
			); err != nil {
				return fmt.Errorf("scan pull request facts: %w", err)
			}
			prState := contract.PRState(state)
			facts[sessionID] = append(facts[sessionID], contract.PRFacts{
				URL:          url,
				Draft:        draft,
				Merged:       prState == contract.PRStateMerged,
				Closed:       prState == contract.PRStateClosed,
				CI:           contract.CIState(ciState),
				Review:       contract.ReviewDecision(reviewState),
				Mergeability: contract.Mergeability(mergeability),
				SourceBranch: sourceBranch,
				TargetBranch: targetBranch,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
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
		var err error
		record, err = scanPullRequest(tx.QueryRow(
			ctx,
			`UPDATE ao_pull_requests
			SET state = $3, draft = $4, head_sha = $5, additions = $6, deletions = $7,
				changed_files = $8, ci_state = $9, review_state = $10, mergeability = $11,
				observed_at = now(), updated_at = now()
			WHERE org_id = $1 AND id = $2
			RETURNING `+pullRequestColumns,
			orgID, pullRequestID,
			string(state), observation.Draft, observation.HeadSHA,
			observation.Additions, observation.Deletions, observation.ChangedFiles,
			string(observation.CIState), string(observation.ReviewState), string(observation.Mergeability),
		))
		return err
	})
	if err != nil {
		return domain.PullRequest{}, normalizeConstraintError(err)
	}
	return record, nil
}

type pullRequestRow interface {
	Scan(dest ...any) error
}

func scanPullRequest(row pullRequestRow) (domain.PullRequest, error) {
	var record domain.PullRequest
	var state, reviewState, mergeability, ciState, aoReviewState string
	err := row.Scan(
		&record.ID, &record.OrgID, &record.SessionID, &record.Provider, &record.Repository,
		&record.Author, &record.Number, &record.URL, &record.Title, &state, &record.Draft, &record.HeadSHA,
		&record.SourceBranch, &record.TargetBranch, &record.Additions, &record.Deletions, &record.ChangedFiles,
		&ciState, &reviewState, &mergeability,
		&record.Checks, &record.ClaimedBySessionID, &record.ClaimedAt, &record.ReleasedAt,
		&aoReviewState, &record.ObservedAt, &record.CreatedAt, &record.UpdatedAt,
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
