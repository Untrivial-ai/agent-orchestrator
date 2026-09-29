package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) SchedulePullRequestRefresh(
	ctx context.Context,
	orgID, pullRequestID string,
	reason domain.PullRequestRefreshReason,
	dueAt time.Time,
	message string,
) error {
	if reason != domain.PullRequestRefreshWebhookFailed && reason != domain.PullRequestRefreshWebhookSilent {
		return ErrInvalid
	}
	return normalizeConstraintError(s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ao_pr_refresh_fallbacks (
				pull_request_id, org_id, due_at, reason, last_error
			) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (pull_request_id) DO UPDATE SET
				due_at = CASE
					WHEN ao_pr_refresh_fallbacks.due_at IS NULL THEN EXCLUDED.due_at
					ELSE LEAST(ao_pr_refresh_fallbacks.due_at, EXCLUDED.due_at)
				END,
				reason = EXCLUDED.reason,
				attempt_count = 0,
				lease_owner = '',
				lease_until = NULL,
				last_error = EXCLUDED.last_error,
				updated_at = now()`,
			pullRequestID, orgID, dueAt, string(reason), boundedError(message))
		return err
	}))
}

func (s *Store) ClaimPullRequestRefresh(
	ctx context.Context,
	owner string,
	now, leaseUntil time.Time,
	silenceGrace time.Duration,
) (domain.PullRequestRefreshJob, error) {
	if strings.TrimSpace(owner) == "" || !leaseUntil.After(now) || silenceGrace <= 0 {
		return domain.PullRequestRefreshJob{}, ErrInvalid
	}
	var job domain.PullRequestRefreshJob
	err := s.withService(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT pull_request.id, pull_request.org_id, pull_request.provider,
					pull_request.repository, pull_request.number,
					COALESCE(NULLIF(fallback.reason, ''), 'webhook_silent') AS reason,
					fallback.due_at
				FROM ao_pull_requests pull_request
				JOIN ao_pr_refresh_fallbacks fallback
					ON fallback.pull_request_id = pull_request.id
				WHERE pull_request.state = 'open'
				  AND (fallback.lease_until IS NULL OR fallback.lease_until <= $2)
				  AND (
					(fallback.due_at IS NOT NULL AND fallback.due_at <= $2)
					OR (fallback.due_at IS NULL AND pull_request.observed_at <= $2 - $4::interval)
				  )
				ORDER BY COALESCE(fallback.due_at, pull_request.observed_at + $4::interval), pull_request.id
				FOR UPDATE OF fallback SKIP LOCKED
				LIMIT 1
			), claimed AS (
				UPDATE ao_pr_refresh_fallbacks fallback
				SET due_at = COALESCE(fallback.due_at, $2),
					reason = candidate.reason,
					attempt_count = fallback.attempt_count + 1,
					lease_owner = $1,
					lease_until = $3,
					updated_at = now()
				FROM candidate
				WHERE fallback.pull_request_id = candidate.id
				RETURNING fallback.pull_request_id, fallback.org_id, fallback.reason,
					fallback.attempt_count, fallback.lease_owner
			)
			SELECT claimed.pull_request_id::text, claimed.org_id::text,
				candidate.provider, candidate.repository, candidate.number,
				claimed.reason, claimed.attempt_count, claimed.lease_owner
			FROM claimed JOIN candidate ON candidate.id = claimed.pull_request_id`,
			owner, now, leaseUntil, intervalString(silenceGrace)).Scan(
			&job.Ref.ID, &job.Ref.OrgID, &job.Ref.Provider, &job.Ref.Repository,
			&job.Ref.Number, &job.Reason, &job.AttemptCount, &job.LeaseOwner)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PullRequestRefreshJob{}, ErrNotFound
	}
	if err != nil {
		return domain.PullRequestRefreshJob{}, normalizeConstraintError(err)
	}
	return job, nil
}

func ensurePullRequestRefreshStateTx(ctx context.Context, tx pgx.Tx, orgID, pullRequestID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO ao_pr_refresh_fallbacks (pull_request_id, org_id)
		VALUES ($1, $2)
		ON CONFLICT (pull_request_id) DO NOTHING`, pullRequestID, orgID)
	return err
}

func (s *Store) RetryPullRequestRefresh(
	ctx context.Context,
	job domain.PullRequestRefreshJob,
	dueAt time.Time,
	message string,
) error {
	return s.withOrg(ctx, job.Ref.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ao_pr_refresh_fallbacks
			SET due_at=$4, lease_owner='', lease_until=NULL, last_error=$5, updated_at=now()
			WHERE org_id=$1 AND pull_request_id=$2 AND lease_owner=$3`,
			job.Ref.OrgID, job.Ref.ID, job.LeaseOwner, dueAt, boundedError(message))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}
