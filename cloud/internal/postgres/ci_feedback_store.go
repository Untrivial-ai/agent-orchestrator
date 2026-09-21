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
	return current.CIState == contract.CIFailing && previous.CIState != contract.CIFailing
}

func shouldResolveCIFailureEffect(previous, current domain.PullRequest) bool {
	return previous.CIState == contract.CIFailing && current.CIState != contract.CIFailing
}

func (s *Store) RecordPullRequestTransition(ctx context.Context, previous, current domain.PullRequest) (domain.SCMEffects, error) {
	var effects domain.SCMEffects
	if shouldResolveCIFailureEffect(previous, current) {
		effects.CIFailureResolved = true
		return effects, s.resolveCIFailureNotification(ctx, previous)
	}
	if !shouldCreateCIFailureEffect(previous, current) {
		return effects, nil
	}
	key := ciFailureApplicationKey(current)
	payload, err := json.Marshal(map[string]any{
		"pullRequestId": current.ID, "pullRequestUrl": current.URL,
		"pullRequestNumber": current.Number, "headSha": current.HeadSHA,
		"repository": current.Repository,
		"message":    fmt.Sprintf("CI failure detected on %s#%d. Inspect the failing checks, fix the cause, and push the correction.", current.Repository, current.Number),
	})
	if err != nil {
		return effects, err
	}
	err = s.withService(ctx, func(tx pgx.Tx) error {
		var inserted bool
		if err := tx.QueryRow(ctx, `
			INSERT INTO ao_github_pr_applications (org_id, pull_request_id, application_key)
			VALUES ($1, $2, $3)
			ON CONFLICT (pull_request_id, application_key) DO NOTHING
			RETURNING true`, current.OrgID, current.ID, key).Scan(&inserted); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		effects.CIFailureStarted = true

		var projectID, recipientID string
		var autoInject bool
		if err := tx.QueryRow(ctx, `
			SELECT project_id::text, COALESCE(created_by_user_id::text, ''), auto_inject_ci
			FROM ao_sessions WHERE org_id = $1 AND id = $2`,
			current.OrgID, current.SessionID).Scan(&projectID, &recipientID, &autoInject); err != nil {
			return err
		}
		if autoInject {
			tag, err := tx.Exec(ctx, `
				INSERT INTO ao_ci_feedback_outbox (
					application_key, org_id, session_id, pull_request_id, payload
				) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (application_key) DO NOTHING`,
				key, current.OrgID, current.SessionID, current.ID, payload)
			if err != nil {
				return err
			}
			effects.FeedbackQueued = tag.RowsAffected() == 1
		}
		if recipientID == "" {
			return nil
		}

		var notificationID string
		err := tx.QueryRow(ctx, `
			INSERT INTO ao_notifications (
				org_id, recipient_user_id, project_id, session_id, pull_request_id,
				source, type, title, body, metadata, dedupe_key, source_event_id, status
			) VALUES ($1, $2, $3, $4, $5, 'cloud', 'ci_failed',
				'CI checks failed', $6, $7, $8, $8, 'unread')
			ON CONFLICT (org_id, recipient_user_id, dedupe_key)
				WHERE resolved_at IS NULL
			DO UPDATE SET body = EXCLUDED.body, metadata = EXCLUDED.metadata,
				source_event_id = EXCLUDED.source_event_id, status = 'unread', updated_at = now()
			RETURNING id::text`,
			current.OrgID, recipientID, projectID, current.SessionID, current.ID,
			fmt.Sprintf("CI is failing for %s#%d.", current.Repository, current.Number),
			payload, key).Scan(&notificationID)
		if err != nil {
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
			) FROM ao_notifications notification
			WHERE org_id = $1 AND id = $2`, current.OrgID, notificationID).Scan(&snapshot); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ao_notification_events (
				org_id, recipient_user_id, notification_id, kind, source_event_id, snapshot
			) VALUES ($1, $2, $3, 'notification_created', $4, $5)`,
			current.OrgID, recipientID, notificationID, key, snapshot); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SELECT pg_notify('ao_notification_event', $1)`, current.OrgID)
		return err
	})
	return effects, err
}

func (s *Store) resolveCIFailureNotification(ctx context.Context, failed domain.PullRequest) error {
	dedupeKey := ciFailureApplicationKey(failed)
	resolutionKey := "ci-resolved:" + failed.ID + ":" + failed.HeadSHA
	return s.withService(ctx, func(tx pgx.Tx) error {
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
	})
}

func (s *Store) ClaimCIFeedback(ctx context.Context, owner string, leaseDuration time.Duration) (domain.CIFeedback, bool, error) {
	var item domain.CIFeedback
	err := s.withService(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
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
				outbox.attempt_count, outbox.lease_owner, outbox.lease_until,
				COALESCE((SELECT worker_id FROM ao_worker_connections
					WHERE org_id = outbox.org_id AND session_id = outbox.session_id
					  AND disconnected_at IS NULL ORDER BY connected_at DESC LIMIT 1), ''),
				COALESCE((SELECT epoch FROM ao_worker_connections
					WHERE org_id = outbox.org_id AND session_id = outbox.session_id
					  AND disconnected_at IS NULL ORDER BY connected_at DESC LIMIT 1), 0)`,
			owner, intervalString(leaseDuration)).Scan(
			&item.ID, &item.ApplicationKey, &item.OrgID, &item.SessionID,
			&item.PullRequestID, &item.Payload, &item.AttemptCount,
			&item.LeaseOwner, &item.LeaseUntil, &item.WorkerID, &item.WorkerEpoch)
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
				next_attempt_at = CASE WHEN $3 = 'retry' THEN $5 ELSE NULL END,
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
