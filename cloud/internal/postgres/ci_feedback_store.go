package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func ciFailureApplicationKey(pr domain.PullRequest) string {
	return fmt.Sprintf("ci-failure:%s:%s", pr.ID, pr.HeadSHA)
}

func shouldCreateCIFailureEffect(previous, current domain.PullRequest) bool {
	return current.CIState == contract.CIFailing &&
		(previous.CIState != contract.CIFailing || previous.HeadSHA != current.HeadSHA)
}

func shouldResolveCIFailureEffect(previous, current domain.PullRequest) bool {
	return previous.CIState == contract.CIFailing && current.CIState != contract.CIFailing
}

func ciFailureMessage(pr domain.PullRequest) string {
	message := fmt.Sprintf(
		"CI failure detected on %s#%d. Inspect the failing checks, fix the cause, and push the correction.",
		pr.Repository, pr.Number,
	)
	var checks []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	}
	if json.Unmarshal(pr.Checks, &checks) != nil {
		return message
	}
	for _, check := range checks {
		switch check.Conclusion {
		case "failure", "timed_out", "action_required", "startup_failure", "cancelled":
			message += "\n- " + check.Name
			if check.HTMLURL != "" {
				message += ": " + check.HTMLURL
			}
		}
	}
	return message
}

func recordPullRequestTransitionTx(
	ctx context.Context,
	tx pgx.Tx,
	previous, current domain.PullRequest,
) (domain.SCMEffects, error) {
	var effects domain.SCMEffects
	if shouldResolveCIFailureEffect(previous, current) {
		effects.CIFailureResolved = true
		return effects, resolveCIFailureNotificationTx(ctx, tx, previous)
	}
	if previous.CIState == contract.CIFailing && current.CIState == contract.CIFailing &&
		previous.HeadSHA != current.HeadSHA {
		if err := resolveCIFailureNotificationTx(ctx, tx, previous); err != nil {
			return effects, err
		}
		effects.CIFailureResolved = true
	}
	if !shouldCreateCIFailureEffect(previous, current) {
		return effects, nil
	}
	key := ciFailureApplicationKey(current)
	payload, err := json.Marshal(map[string]any{
		"pullRequestId": current.ID, "pullRequestUrl": current.URL,
		"pullRequestNumber": current.Number, "headSha": current.HeadSHA,
		"repository": current.Repository,
		"message":    ciFailureMessage(current),
	})
	if err != nil {
		return effects, err
	}
	var inserted bool
	if err := tx.QueryRow(ctx, `
			INSERT INTO ao_github_pr_applications (org_id, pull_request_id, application_key)
			VALUES ($1, $2, $3)
			ON CONFLICT (pull_request_id, application_key) DO NOTHING
			RETURNING true`, current.OrgID, current.ID, key).Scan(&inserted); errors.Is(err, pgx.ErrNoRows) {
		return effects, nil
	} else if err != nil {
		return effects, err
	}
	effects.CIFailureStarted = true

	var autoInject bool
	if err := tx.QueryRow(ctx, `
			SELECT auto_inject_ci
			FROM ao_sessions WHERE org_id = $1 AND id = $2`,
		current.OrgID, current.SessionID).Scan(&autoInject); err != nil {
		return effects, err
	}
	if autoInject {
		tag, err := tx.Exec(ctx, `
				INSERT INTO ao_ci_feedback_outbox (
					application_key, org_id, session_id, pull_request_id, payload
				) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (application_key) DO NOTHING`,
			key, current.OrgID, current.SessionID, current.ID, payload)
		if err != nil {
			return effects, err
		}
		effects.FeedbackQueued = tag.RowsAffected() == 1
	}
	return effects, nil
}

func resolveCIFailureNotificationTx(ctx context.Context, tx pgx.Tx, failed domain.PullRequest) error {
	dedupeKey := ciFailureApplicationKey(failed)
	resolutionKey := "ci-resolved:" + failed.ID + ":" + failed.HeadSHA
	var sessionRecipientID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(created_by_user_id::text, '')
		FROM ao_sessions WHERE org_id = $1 AND id = $2`,
		failed.OrgID, failed.SessionID).Scan(&sessionRecipientID); err != nil {
		return err
	}
	if sessionRecipientID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true)`, sessionRecipientID); err != nil {
		return err
	}
	var inserted bool
	if err := tx.QueryRow(ctx, `
			INSERT INTO ao_github_pr_applications (org_id, pull_request_id, application_key)
			VALUES ($1, $2, $3)
			ON CONFLICT (pull_request_id, application_key) DO NOTHING
			RETURNING true`, failed.OrgID, failed.ID, resolutionKey).Scan(&inserted); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var notificationID, recipientID string
	var snapshot []byte
	err := tx.QueryRow(ctx, `
			WITH resolved AS (
				UPDATE ao_notifications
				SET resolved_at = now(), updated_at = now()
				WHERE org_id = $1 AND pull_request_id = $2 AND dedupe_key = $3
				  AND resolved_at IS NULL
				RETURNING *
			)
			SELECT id::text, recipient_user_id::text, jsonb_build_object(
				'id', id::text, 'orgId', org_id::text,
				'recipientUserId', recipient_user_id::text,
				'projectId', project_id::text, 'sessionId', session_id::text,
				'source', source, 'type', type, 'title', title, 'body', body,
				'status', status, 'eventId', source_event_id, 'metadata', metadata,
				'resolvedAt', resolved_at, 'createdAt', created_at, 'updatedAt', updated_at
			) FROM resolved`, failed.OrgID, failed.ID, dedupeKey).Scan(&notificationID, &recipientID, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
			INSERT INTO ao_notification_events (
				org_id, recipient_user_id, notification_id, kind, source_event_id, snapshot
			) VALUES ($1, $2, $3, 'notification_resolved', $4, $5)`,
		failed.OrgID, recipientID, notificationID, resolutionKey, snapshot); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify('ao_notification_event', $1)`, failed.OrgID)
	return err
}

func (s *Store) ClaimCIFeedback(ctx context.Context, owner string, leaseDuration time.Duration) (domain.CIFeedback, bool, error) {
	var item domain.CIFeedback
	err := s.withService(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT id FROM ao_ci_feedback_outbox
				WHERE (status IN ('pending', 'retry') AND COALESCE(next_attempt_at, created_at) <= now())
				   OR (status = 'processing' AND lease_until < now())
				ORDER BY COALESCE(next_attempt_at, created_at), created_at
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE ao_ci_feedback_outbox outbox
			SET status = 'processing', lease_owner = $1,
				lease_until = now() + $2::interval,
				attempt_count = attempt_count + 1, updated_at = now()
			FROM candidate WHERE outbox.id = candidate.id
			RETURNING outbox.id::text, outbox.application_key, outbox.org_id::text,
				outbox.session_id::text, outbox.pull_request_id::text, outbox.payload,
				outbox.attempt_count, outbox.lease_owner, outbox.lease_until`,
			owner, intervalString(leaseDuration)).Scan(
			&item.ID, &item.ApplicationKey, &item.OrgID, &item.SessionID,
			&item.PullRequestID, &item.Payload, &item.AttemptCount,
			&item.LeaseOwner, &item.LeaseUntil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, item.OrgID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT
			COALESCE((SELECT worker_id FROM ao_worker_connections
				WHERE org_id = $1 AND session_id = $2 AND disconnected_at IS NULL
				ORDER BY connected_at DESC LIMIT 1), ''),
			COALESCE((SELECT epoch FROM ao_worker_connections
				WHERE org_id = $1 AND session_id = $2 AND disconnected_at IS NULL
				ORDER BY connected_at DESC LIMIT 1), 0)`, item.OrgID, item.SessionID).Scan(
			&item.WorkerID, &item.WorkerEpoch)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CIFeedback{}, false, nil
	}
	if err != nil {
		return domain.CIFeedback{}, false, err
	}
	return item, true, nil
}

func (s *Store) CompleteCIFeedback(ctx context.Context, id, owner string) error {
	return s.finishCIFeedback(ctx, id, owner, "delivered", "", time.Time{})
}

func (s *Store) RetryCIFeedback(ctx context.Context, id, owner, message string, retryAt time.Time) error {
	return s.finishCIFeedback(ctx, id, owner, "retry", message, retryAt)
}

func (s *Store) finishCIFeedback(ctx context.Context, id, owner, status, message string, retryAt time.Time) error {
	return s.withService(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ao_ci_feedback_outbox
			SET status = $3, lease_owner = '', lease_until = NULL, last_error = $4,
				next_attempt_at = CASE WHEN $3 = 'retry' THEN $5::timestamptz ELSE NULL END,
				delivered_at = CASE WHEN $3 = 'delivered' THEN now() ELSE delivered_at END,
				updated_at = now()
			WHERE id = $1 AND lease_owner = $2`, id, owner, status, message, retryAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}
