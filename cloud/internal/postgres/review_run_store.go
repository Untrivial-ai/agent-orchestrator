package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const reviewTerminalRequestTTL = 30 * time.Second

// CreateReviewRun records at most one review pass per pull request commit.
func (s *Store) CreateReviewRun(
	ctx context.Context,
	orgID, pullRequestID, reviewSessionID, targetSHA string,
) (run domain.ReviewRun, created bool, err error) {
	err = s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		row := tx.QueryRow(
			ctx,
			`INSERT INTO ao_review_runs (org_id, pull_request_id, review_session_id, target_sha)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (pull_request_id, target_sha) DO NOTHING
			RETURNING `+reviewRunColumns,
			orgID, pullRequestID, reviewSessionID, targetSHA,
		)
		var scanErr error
		run, scanErr = scanReviewRun(row)
		if errors.Is(scanErr, ErrNotFound) {
			row := tx.QueryRow(
				ctx,
				`SELECT `+reviewRunColumns+`
				FROM ao_review_runs
				WHERE org_id = $1 AND pull_request_id = $2 AND target_sha = $3`,
				orgID, pullRequestID, targetSHA,
			)
			run, scanErr = scanReviewRun(row)
			created = false
			return scanErr
		}
		if scanErr != nil {
			return scanErr
		}
		created = true
		_, err := tx.Exec(
			ctx,
			`UPDATE ao_pull_requests
			SET ao_review_state = 'running', updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, pullRequestID,
		)
		return err
	})
	if err != nil {
		return domain.ReviewRun{}, false, normalizeConstraintError(err)
	}
	return run, created, nil
}

// OpenReviewTerminal starts a dedicated review process in the session sandbox.
func (s *Store) OpenReviewTerminal(
	ctx context.Context,
	orgID, sessionID, reviewRunID, prompt string,
) error {
	terminalID := uuid.NewString()
	// Separate transactions guarantee that open sorts before input by created_at.
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET desired_state = 'running', startup_started_at = now(),
				reconcile_after = now(), updated_at = now()
			WHERE session_id = $1 AND org_id = $2 AND desired_state = 'paused'`,
			sessionID, orgID,
		); err != nil {
			return err
		}
		openPayload, err := json.Marshal(worker.TerminalCommand{TerminalID: terminalID, Kind: "agent"})
		if err != nil {
			return err
		}
		if _, err := createWorkerRequest(
			ctx, tx, orgID, sessionID, "terminal.open", openPayload, reviewTerminalRequestTTL, "",
		); err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_review_runs SET review_terminal_id = $3 WHERE org_id = $1 AND id = $2`,
			orgID, reviewRunID, terminalID,
		)
		return err
	})
	if err != nil {
		return err
	}
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		inputPayload, err := json.Marshal(worker.TerminalCommand{
			TerminalID: terminalID, Data: []byte(prompt + "\r"),
		})
		if err != nil {
			return err
		}
		_, err = createWorkerRequest(
			ctx, tx, orgID, sessionID, "terminal.input", inputPayload, reviewTerminalRequestTTL, "",
		)
		return err
	})
}

// CloseReviewTerminal tears down the dedicated review process.
func (s *Store) CloseReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID string) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var terminalID *string
		if err := tx.QueryRow(
			ctx,
			`SELECT review_terminal_id FROM ao_review_runs WHERE org_id = $1 AND id = $2`,
			orgID, reviewRunID,
		).Scan(&terminalID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if terminalID == nil {
			return nil
		}
		payload, err := json.Marshal(worker.TerminalCommand{TerminalID: *terminalID})
		if err != nil {
			return err
		}
		_, err = createWorkerRequest(ctx, tx, orgID, sessionID, "terminal.close", payload, reviewTerminalRequestTTL, "")
		if errors.Is(err, ErrWorkerUnavailable) {
			return nil
		}
		return err
	})
}

// CompleteAndDeliverReviewRun records a delivered verdict from the owning session.
func (s *Store) CompleteAndDeliverReviewRun(
	ctx context.Context,
	orgID, reviewRunID, reviewSessionID string,
	result domain.SubmitReviewResult,
	providerReviewID string,
) (domain.ReviewRun, error) {
	var run domain.ReviewRun
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		row := tx.QueryRow(
			ctx,
			`UPDATE ao_review_runs
			SET status = 'delivered', verdict = $4, body = $5, provider_review_id = $6,
				completed_at = now(), delivered_at = now()
			WHERE org_id = $1 AND id = $2 AND review_session_id = $3 AND status = 'running'
			RETURNING `+reviewRunColumns,
			orgID, reviewRunID, reviewSessionID, string(result.Verdict), result.Body, providerReviewID,
		)
		var err error
		run, err = scanReviewRun(row)
		if err != nil {
			return err
		}
		aoReviewState := "up_to_date"
		if result.Verdict == contract.AOReviewVerdictChangesRequested {
			aoReviewState = "changes_requested"
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_pull_requests
			SET ao_review_state = $3, updated_at = now()
			WHERE org_id = $1 AND id = $2 AND head_sha = $4`,
			orgID, run.PullRequestID, aoReviewState, run.TargetSHA,
		)
		return err
	})
	if err != nil {
		return domain.ReviewRun{}, normalizeConstraintError(err)
	}
	return run, nil
}

// FailReviewRun records a failed review pass from the owning session.
func (s *Store) FailReviewRun(
	ctx context.Context,
	orgID, reviewRunID, reviewSessionID, lastError string,
) (domain.ReviewRun, error) {
	var run domain.ReviewRun
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		row := tx.QueryRow(
			ctx,
			`UPDATE ao_review_runs
			SET status = 'failed', last_error = $4, completed_at = now()
			WHERE org_id = $1 AND id = $2 AND review_session_id = $3 AND status = 'running'
			RETURNING `+reviewRunColumns,
			orgID, reviewRunID, reviewSessionID, lastError,
		)
		var err error
		run, err = scanReviewRun(row)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`UPDATE ao_pull_requests
			SET ao_review_state = 'needs_review', updated_at = now()
			WHERE org_id = $1 AND id = $2 AND head_sha = $3`,
			orgID, run.PullRequestID, run.TargetSHA,
		)
		return err
	})
	if err != nil {
		return domain.ReviewRun{}, normalizeConstraintError(err)
	}
	return run, nil
}

// ReviewRunPullRequest returns a review run joined with its pull request.
func (s *Store) ReviewRunPullRequest(
	ctx context.Context,
	orgID, reviewRunID string,
) (domain.ReviewRunPullRequest, error) {
	var out domain.ReviewRunPullRequest
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		row := tx.QueryRow(
			ctx,
			`SELECT run.id, run.org_id, run.pull_request_id, run.review_session_id,
				run.target_sha, run.status, run.verdict, run.body,
				run.provider_review_id, run.last_error, run.created_at,
				run.completed_at, run.delivered_at,
				pr.provider, pr.repository, pr.number, pr.url, pr.title, pr.ao_review_state
			FROM ao_review_runs run
			JOIN ao_pull_requests pr ON pr.org_id = run.org_id AND pr.id = run.pull_request_id
			WHERE run.org_id = $1 AND run.id = $2`,
			orgID, reviewRunID,
		)
		var status, verdict, aoReviewState string
		if err := row.Scan(
			&out.ID, &out.OrgID, &out.PullRequestID, &out.ReviewSessionID,
			&out.TargetSHA, &status, &verdict, &out.Body,
			&out.ProviderReviewID, &out.LastError, &out.CreatedAt,
			&out.CompletedAt, &out.DeliveredAt,
			&out.PullRequestProvider, &out.PullRequestRepository, &out.PullRequestNumber,
			&out.PullRequestURL, &out.PullRequestTitle, &aoReviewState,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("scan review run pull request: %w", err)
		}
		out.Status = contract.AOReviewRunStatus(status)
		out.Verdict = contract.AOReviewVerdict(verdict)
		out.PullRequestAOReviewState = contract.AOReviewState(aoReviewState)
		return nil
	})
	if err != nil {
		return domain.ReviewRunPullRequest{}, err
	}
	return out, nil
}

// ListReviewRunsBySession returns a session's review runs, newest first.
func (s *Store) ListReviewRunsBySession(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) ([]domain.ReviewRunPullRequest, error) {
	var out []domain.ReviewRunPullRequest
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		rows, err := tx.Query(
			ctx,
			`SELECT run.id, run.org_id, run.pull_request_id, run.review_session_id,
				run.target_sha, run.status, run.verdict, run.body,
				run.provider_review_id, run.last_error, run.created_at,
				run.completed_at, run.delivered_at,
				pr.provider, pr.repository, pr.number, pr.url, pr.title, pr.ao_review_state
			FROM ao_review_runs run
			JOIN ao_pull_requests pr ON pr.org_id = run.org_id AND pr.id = run.pull_request_id
			WHERE run.org_id = $1
				AND (
					pr.session_id = $2
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
			ORDER BY run.pull_request_id, run.created_at DESC`,
			orgID, sessionID,
		)
		if err != nil {
			return fmt.Errorf("list review runs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var run domain.ReviewRunPullRequest
			var status, verdict, aoReviewState string
			if err := rows.Scan(
				&run.ID, &run.OrgID, &run.PullRequestID, &run.ReviewSessionID,
				&run.TargetSHA, &status, &verdict, &run.Body,
				&run.ProviderReviewID, &run.LastError, &run.CreatedAt,
				&run.CompletedAt, &run.DeliveredAt,
				&run.PullRequestProvider, &run.PullRequestRepository, &run.PullRequestNumber,
				&run.PullRequestURL, &run.PullRequestTitle, &aoReviewState,
			); err != nil {
				return fmt.Errorf("scan review run: %w", err)
			}
			run.Status = contract.AOReviewRunStatus(status)
			run.Verdict = contract.AOReviewVerdict(verdict)
			run.PullRequestAOReviewState = contract.AOReviewState(aoReviewState)
			out = append(out, run)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const reviewRunColumns = `id, org_id, pull_request_id, review_session_id, target_sha,
	status, verdict, body, provider_review_id, last_error, created_at, completed_at, delivered_at`

type reviewRunRow interface {
	Scan(dest ...any) error
}

func scanReviewRun(row reviewRunRow) (domain.ReviewRun, error) {
	var run domain.ReviewRun
	var status, verdict string
	err := row.Scan(
		&run.ID, &run.OrgID, &run.PullRequestID, &run.ReviewSessionID, &run.TargetSHA,
		&status, &verdict, &run.Body, &run.ProviderReviewID, &run.LastError,
		&run.CreatedAt, &run.CompletedAt, &run.DeliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ReviewRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ReviewRun{}, fmt.Errorf("scan review run: %w", err)
	}
	run.Status = contract.AOReviewRunStatus(status)
	run.Verdict = contract.AOReviewVerdict(verdict)
	return run, nil
}
