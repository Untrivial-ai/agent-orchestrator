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

const (
	reviewTerminalRequestTTL = 30 * time.Second
	reviewTerminalSessionTTL = 24 * time.Hour
)

// CreateReviewRun records at most one active review pass per pull request
// commit. Terminal runs are retained as history and can be retriggered.
func (s *Store) CreateReviewRun(
	ctx context.Context,
	orgID, pullRequestID, reviewSessionID, targetSHA, harness string,
) (run domain.ReviewRun, created bool, err error) {
	err = s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		row := tx.QueryRow(
			ctx,
			`INSERT INTO ao_review_runs (org_id, pull_request_id, review_session_id, target_sha, harness)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (pull_request_id, target_sha) WHERE status = 'running' DO NOTHING
			RETURNING `+reviewRunColumns,
			orgID, pullRequestID, reviewSessionID, targetSHA, harness,
		)
		var scanErr error
		run, scanErr = scanReviewRun(row)
		if errors.Is(scanErr, ErrNotFound) {
			row := tx.QueryRow(
				ctx,
				`SELECT `+reviewRunColumns+`
				FROM ao_review_runs
				WHERE org_id = $1 AND pull_request_id = $2 AND target_sha = $3
				  AND status = 'running'`,
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
	orgID, sessionID, reviewRunID, prompt, harness string,
) (string, error) {
	terminalID := uuid.NewString()
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
		openPayload, err := json.Marshal(reviewTerminalOpenCommand(terminalID, prompt, harness))
		if err != nil {
			return err
		}
		request, err := createWorkerRequest(
			ctx, tx, orgID, sessionID, "terminal.open", openPayload, reviewTerminalRequestTTL, "",
		)
		if err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM ao_terminal_sessions
			WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
			  AND state IN ('opening', 'open') AND expires_at > now()`,
			orgID, sessionID, request.WorkerEpoch,
		).Scan(&active); err != nil {
			return err
		}
		if active >= maxActiveTerminalSessions {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ao_terminal_sessions (
				id, org_id, session_id, worker_epoch, kind, expires_at
			) VALUES ($1, $2, $3, $4, 'agent', now() + $5::interval)`,
			terminalID, orgID, sessionID, request.WorkerEpoch, intervalString(reviewTerminalSessionTTL),
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
		return "", err
	}
	return terminalID, nil
}

func reviewTerminalOpenCommand(terminalID, prompt, harness string) worker.TerminalCommand {
	// Start Codex with the prompt instead of sending terminal input after
	// launch. Its interactive UI can take longer than a fixed delay to
	// initialize, causing an early terminal write to be discarded.
	return worker.TerminalCommand{
		TerminalID: terminalID,
		Kind:       "agent",
		Review:     true,
		Harness:    harness,
		Data:       []byte(prompt),
	}
}

// CheckSessionWriteAccess authorizes a request that mutates a session-owned
// review. Read-only share grants must be able to inspect review state without
// being able to start or cancel the review.
func (s *Store) CheckSessionWriteAccess(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) error {
	return s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" {
			return ErrForbidden
		}
		var mode string
		if err := tx.QueryRow(
			ctx,
			`SELECT mode FROM ao_sessions WHERE org_id = $1 AND id = $2`,
			orgID, sessionID,
		).Scan(&mode); err != nil {
			return err
		}
		if effectiveMode(mode, access.ModeCap) == "read-only" {
			return ErrForbidden
		}
		return nil
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

// CancelRunningReviewRunsBySession records user cancellation before any
// terminal teardown. A worker can be disconnected, but its review must never
// remain rendered as running merely because it missed the close request.
func (s *Store) CancelRunningReviewRunsBySession(
	ctx context.Context,
	orgID, sessionID string,
) ([]domain.ReviewRun, error) {
	var runs []domain.ReviewRun
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE ao_review_runs run
			SET status = 'cancelled', completed_at = now(), last_error = 'cancelled by user'
			WHERE run.org_id = $1
				AND run.review_session_id = $2
				AND run.status = 'running'
			RETURNING `+reviewRunColumns, orgID, sessionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			run, err := scanReviewRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, run := range runs {
			if _, err := tx.Exec(ctx, `
				UPDATE ao_pull_requests
				SET ao_review_state = 'needs_review', updated_at = now()
				WHERE org_id = $1 AND id = $2 AND head_sha = $3`, orgID, run.PullRequestID, run.TargetSHA); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, normalizeConstraintError(err)
	}
	return runs, nil
}

// CancelReviewRuns records cancellation for only the runs started by one
// trigger request. This rolls back a multi-PR trigger when a later terminal
// cannot be opened, without cancelling reviews that predated the request.
func (s *Store) CancelReviewRuns(
	ctx context.Context,
	orgID, sessionID string,
	runIDs []string,
) ([]domain.ReviewRun, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	var runs []domain.ReviewRun
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE ao_review_runs run
			SET status = 'cancelled', completed_at = now(), last_error = 'cancelled after review batch failure'
			WHERE run.org_id = $1
				AND run.review_session_id = $2
				AND run.id = ANY($3::uuid[])
				AND run.status = 'running'
			RETURNING `+reviewRunColumns, orgID, sessionID, runIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanReviewRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, run := range runs {
			if _, err := tx.Exec(ctx, `
				UPDATE ao_pull_requests
				SET ao_review_state = 'needs_review', updated_at = now()
				WHERE org_id = $1 AND id = $2 AND head_sha = $3`,
				orgID, run.PullRequestID, run.TargetSHA); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, normalizeConstraintError(err)
	}
	return runs, nil
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
				run.target_sha, run.harness, run.status, run.verdict, run.body,
				run.provider_review_id, run.review_terminal_id, run.last_error, run.created_at,
				run.completed_at, run.delivered_at,
				pr.provider, pr.repository, pr.number, pr.url, pr.title, pr.ao_review_state
			FROM ao_review_runs run
			JOIN ao_pull_requests pr ON pr.org_id = run.org_id AND pr.id = run.pull_request_id
			WHERE run.org_id = $1 AND run.id = $2`,
			orgID, reviewRunID,
		)
		var status, verdict, aoReviewState string
		var reviewTerminalID *string
		if err := row.Scan(
			&out.ID, &out.OrgID, &out.PullRequestID, &out.ReviewSessionID,
			&out.TargetSHA, &out.Harness, &status, &verdict, &out.Body,
			&out.ProviderReviewID, &reviewTerminalID, &out.LastError, &out.CreatedAt,
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
		if reviewTerminalID != nil {
			out.ReviewTerminalID = *reviewTerminalID
		}
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
				run.provider_review_id, run.review_terminal_id, run.last_error, run.created_at,
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
			var reviewTerminalID *string
			if err := rows.Scan(
				&run.ID, &run.OrgID, &run.PullRequestID, &run.ReviewSessionID,
				&run.TargetSHA, &run.Harness, &status, &verdict, &run.Body,
				&run.ProviderReviewID, &reviewTerminalID, &run.LastError, &run.CreatedAt,
				&run.CompletedAt, &run.DeliveredAt,
				&run.PullRequestProvider, &run.PullRequestRepository, &run.PullRequestNumber,
				&run.PullRequestURL, &run.PullRequestTitle, &aoReviewState,
			); err != nil {
				return fmt.Errorf("scan review run: %w", err)
			}
			run.Status = contract.AOReviewRunStatus(status)
			run.Verdict = contract.AOReviewVerdict(verdict)
			if reviewTerminalID != nil {
				run.ReviewTerminalID = *reviewTerminalID
			}
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

const reviewRunColumns = `id, org_id, pull_request_id, review_session_id, target_sha, harness,
	status, verdict, body, provider_review_id, review_terminal_id, last_error, created_at, completed_at, delivered_at`

type reviewRunRow interface {
	Scan(dest ...any) error
}

func scanReviewRun(row reviewRunRow) (domain.ReviewRun, error) {
	var run domain.ReviewRun
	var status, verdict string
	var reviewTerminalID *string
	err := row.Scan(
		&run.ID, &run.OrgID, &run.PullRequestID, &run.ReviewSessionID, &run.TargetSHA, &run.Harness,
		&status, &verdict, &run.Body, &run.ProviderReviewID, &reviewTerminalID, &run.LastError,
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
	if reviewTerminalID != nil {
		run.ReviewTerminalID = *reviewTerminalID
	}
	return run, nil
}
