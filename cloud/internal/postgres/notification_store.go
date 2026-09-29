package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) AcceptNotificationEvent(
	ctx context.Context,
	orgID, sessionID, workerID string,
	workerEpoch int64,
	event domain.AgentNotificationEvent,
) (domain.NotificationAcceptance, error) {
	var accepted domain.NotificationAcceptance
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var projectID, recipientUserID string
		err := tx.QueryRow(ctx, `
			SELECT session.project_id::text, COALESCE(session.created_by_user_id::text, '')
			FROM ao_sessions session
			JOIN ao_worker_connections worker
			  ON worker.org_id = session.org_id AND worker.session_id = session.id
			WHERE session.org_id = $1 AND session.id = $2
			  AND worker.worker_id = $3 AND worker.epoch = $4
			  AND worker.disconnected_at IS NULL
			FOR UPDATE OF worker`, orgID, sessionID, workerID, workerEpoch,
		).Scan(&projectID, &recipientUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleWorker
		}
		if err != nil {
			return fmt.Errorf("validate notification worker: %w", err)
		}

		err = tx.QueryRow(ctx, `
			INSERT INTO ao_notification_ingress (
				org_id, project_id, session_id, recipient_user_id,
				worker_id, worker_epoch, event_id, event_type, payload, occurred_at
			) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (worker_id, worker_epoch, event_id) DO NOTHING
			RETURNING id::text`,
			orgID, projectID, sessionID, recipientUserID, workerID, workerEpoch,
			event.EventID, string(event.Type), event.Payload, event.OccurredAt,
		).Scan(&accepted.IngressID)
		if err == nil {
			accepted.EventID = event.EventID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return normalizeConstraintError(err)
		}

		var existingType string
		var existingPayload json.RawMessage
		var existingOccurredAt time.Time
		if err := tx.QueryRow(ctx, `
			SELECT id::text, event_type, payload, occurred_at
			FROM ao_notification_ingress
			WHERE worker_id = $1 AND worker_epoch = $2 AND event_id = $3`,
			workerID, workerEpoch, event.EventID,
		).Scan(&accepted.IngressID, &existingType, &existingPayload, &existingOccurredAt); err != nil {
			return fmt.Errorf("load duplicate notification event: %w", err)
		}
		if existingType != string(event.Type) ||
			!jsonEqual(existingPayload, event.Payload) ||
			!existingOccurredAt.Equal(event.OccurredAt) {
			return ErrIdempotencyMismatch
		}
		accepted.EventID = event.EventID
		accepted.Duplicate = true
		return nil
	})
	return accepted, err
}

func (s *Store) ClaimNotificationEvent(
	ctx context.Context,
	leaseOwner string,
	leaseDuration time.Duration,
) (domain.NotificationIngress, bool, error) {
	var ingress domain.NotificationIngress
	found := false
	err := s.withService(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT id FROM ao_notification_ingress
				WHERE (status = 'pending'
				   OR (status = 'claimed' AND lease_until <= now()))
				  AND next_attempt_at <= now()
				ORDER BY next_attempt_at, created_at, id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE ao_notification_ingress ingress
			SET status = 'claimed', lease_owner = $1,
				lease_until = now() + $2::interval,
				attempt_count = ingress.attempt_count + 1,
				updated_at = now()
			FROM candidate
			WHERE ingress.id = candidate.id
			RETURNING ingress.id::text, ingress.org_id::text,
				ingress.project_id::text, ingress.session_id::text,
				COALESCE(ingress.recipient_user_id::text, ''),
				ingress.worker_id, ingress.worker_epoch,
				ingress.event_id, ingress.event_type, ingress.occurred_at,
				ingress.payload, ingress.attempt_count,
				COALESCE(ingress.lease_owner, ''), ingress.lease_until,
				ingress.created_at`, leaseOwner, intervalString(leaseDuration),
		).Scan(
			&ingress.ID, &ingress.OrgID, &ingress.ProjectID, &ingress.SessionID,
			&ingress.RecipientUserID, &ingress.WorkerID, &ingress.WorkerEpoch,
			&ingress.Event.EventID, &ingress.Event.Type, &ingress.Event.OccurredAt,
			&ingress.Event.Payload, &ingress.AttemptCount, &ingress.LeaseOwner,
			&ingress.LeaseUntil, &ingress.CreatedAt,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotificationIngress{}, false, nil
	}
	if err != nil {
		return domain.NotificationIngress{}, false, err
	}
	found = true
	return ingress, found, nil
}

func (s *Store) CompleteNotificationEvent(ctx context.Context, orgID, ingressID, leaseOwner string) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ao_notification_ingress
			SET status = 'complete', lease_owner = NULL, lease_until = NULL,
				last_error = '', updated_at = now()
			WHERE org_id = $1 AND id = $2 AND status = 'claimed' AND lease_owner = $3`,
			orgID, ingressID, leaseOwner)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrConflict
		}
		return nil
	})
}

func (s *Store) RetryNotificationEvent(
	ctx context.Context,
	orgID, ingressID, leaseOwner, message string,
	nextAttemptAt time.Time,
	terminal bool,
) error {
	status := "pending"
	if terminal {
		status = "failed"
	}
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ao_notification_ingress
			SET status = $4, lease_owner = NULL, lease_until = NULL,
				next_attempt_at = $5, last_error = $6, updated_at = now()
			WHERE org_id = $1 AND id = $2 AND status = 'claimed' AND lease_owner = $3`,
			orgID, ingressID, leaseOwner, status, nextAttemptAt, message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrConflict
		}
		return nil
	})
}

func (s *Store) CreateNotificationFromIngress(
	ctx context.Context,
	ingress domain.NotificationIngress,
	input domain.Notification,
) (domain.Notification, bool, error) {
	var result domain.Notification
	changed := false
	err := s.withService(ctx, func(tx pgx.Tx) error {
		var existing domain.Notification
		err := scanNotification(tx.QueryRow(ctx, notificationSelect+`
			WHERE notification.org_id = $1
			  AND notification.recipient_user_id = $2
			  AND notification.dedupe_key = $3
			  AND notification.resolved_at IS NULL
			FOR UPDATE`, ingress.OrgID, ingress.RecipientUserID, input.DedupeKey), &existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		kind := domain.NotificationEventCreated
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
				INSERT INTO ao_notifications (
					org_id, recipient_user_id, project_id, session_id, source,
					type, title, body, metadata, dedupe_key, source_event_id, status
				) VALUES ($1, $2, $3, $4, 'cloud', $5, $6, $7, $8, $9, $10, $11)
				RETURNING `+notificationReturning,
				ingress.OrgID, ingress.RecipientUserID, ingress.ProjectID, ingress.SessionID,
				input.Type, input.Title, input.Body, input.Metadata, input.DedupeKey,
				ingress.Event.EventID, input.Status,
			).Scan(notificationScanTargets(&result)...)
			if err != nil {
				return normalizeConstraintError(err)
			}
			changed = true
		} else {
			result = existing
			if existing.Type != input.Type || existing.Title != input.Title ||
				existing.Body != input.Body || existing.Status != input.Status ||
				existing.EventID != ingress.Event.EventID ||
				!jsonEqual(existing.Metadata, input.Metadata) {
				kind = domain.NotificationEventUpdated
				err = tx.QueryRow(ctx, `
					UPDATE ao_notifications
					SET type = $4, title = $5, body = $6, metadata = $7,
						source_event_id = $8, status = $9, updated_at = now()
					WHERE org_id = $1 AND id = $2 AND recipient_user_id = $3
					RETURNING `+notificationReturning,
					ingress.OrgID, existing.ID, ingress.RecipientUserID,
					input.Type, input.Title, input.Body, input.Metadata,
					ingress.Event.EventID, input.Status,
				).Scan(notificationScanTargets(&result)...)
				if err != nil {
					return err
				}
				changed = true
			}
		}

		if changed {
			snapshot, err := json.Marshal(result)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ao_notification_events (
					org_id, recipient_user_id, notification_id, kind, source_event_id, snapshot
				) VALUES ($1, $2, $3, $4, $5, $6)`,
				ingress.OrgID, ingress.RecipientUserID, result.ID, string(kind),
				ingress.Event.EventID, snapshot,
			); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE ao_notification_ingress
			SET status = 'complete', lease_owner = NULL, lease_until = NULL,
				last_error = '', updated_at = now()
			WHERE org_id = $1 AND id = $2
			  AND ($3 = '' OR lease_owner = $3)`,
			ingress.OrgID, ingress.ID, ingress.LeaseOwner)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrConflict
		}
		if changed {
			if _, err := tx.Exec(ctx, `SELECT pg_notify('ao_notification_event', $1)`, ingress.OrgID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, changed, err
}

func (s *Store) ListNotifications(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	filter domain.NotificationFilter,
) (domain.NotificationPage, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 50
	}
	var page domain.NotificationPage
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		args := []any{orgID, principal.UserID, filter.Limit + 1}
		where := `WHERE notification.org_id = $1 AND notification.recipient_user_id = $2`
		if filter.Status.Valid() {
			args = append(args, string(filter.Status))
			where += fmt.Sprintf(" AND notification.status = $%d", len(args))
		}
		if filter.Cursor != nil {
			args = append(args, filter.Cursor.CreatedAt, filter.Cursor.ID)
			where += fmt.Sprintf(" AND (notification.created_at, notification.id) < ($%d, $%d::uuid)", len(args)-1, len(args))
		}
		rows, err := tx.Query(ctx, notificationSelect+" "+where+`
			ORDER BY notification.created_at DESC, notification.id DESC
			LIMIT $3`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.Notification
			if err := rows.Scan(notificationScanTargets(&item)...); err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > filter.Limit {
			page.HasMore = true
			page.Items = page.Items[:filter.Limit]
		}
		if len(page.Items) > 0 {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = &domain.NotificationCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status = 'unread'),
				COALESCE((SELECT max(sequence) FROM ao_notification_events
					WHERE org_id = $1 AND recipient_user_id = $2), 0)
			FROM ao_notifications
			WHERE org_id = $1 AND recipient_user_id = $2`, orgID, principal.UserID,
		).Scan(&page.UnreadCount, &page.LatestSequence)
	})
	return page, err
}

func (s *Store) ListNotificationEvents(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	after int64,
	limit int,
) ([]domain.NotificationEvent, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	events := make([]domain.NotificationEvent, 0, limit)
	hasMore := false
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sequence, org_id::text, recipient_user_id::text,
				kind, source_event_id, snapshot, created_at
			FROM ao_notification_events
			WHERE org_id = $1 AND recipient_user_id = $2 AND sequence > $3
			ORDER BY sequence
			LIMIT $4`, orgID, principal.UserID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event domain.NotificationEvent
			var snapshot json.RawMessage
			if err := rows.Scan(&event.Sequence, &event.OrgID, &event.RecipientUserID,
				&event.Kind, &event.EventID, &snapshot, &event.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(snapshot, &event.Notification); err != nil {
				return err
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	if len(events) > limit {
		hasMore = true
		events = events[:limit]
	}
	return events, hasMore, err
}

func (s *Store) MarkNotificationsRead(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	ids []string,
) (int64, error) {
	var changed int64
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if ids != nil && len(ids) == 0 {
			return nil
		}
		query := `
			UPDATE ao_notifications
			SET status = 'read', updated_at = now()
			WHERE org_id = $1 AND recipient_user_id = $2
			  AND status = 'unread'`
		args := []any{orgID, principal.UserID}
		if ids != nil {
			query += ` AND id = ANY($3::uuid[])`
			args = append(args, ids)
		}
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return normalizeConstraintError(err)
		}
		changed = tag.RowsAffected()
		return nil
	})
	return changed, err
}

const notificationSelect = `SELECT ` + notificationColumns + ` FROM ao_notifications notification`

const notificationColumns = `notification.id::text, notification.org_id::text,
	notification.recipient_user_id::text, notification.project_id::text,
	notification.session_id::text, notification.source, notification.type,
	notification.title, notification.body, notification.dedupe_key,
	notification.status, notification.source_event_id, notification.metadata,
	notification.resolved_at, notification.created_at, notification.updated_at`

const notificationReturning = `id::text, org_id::text, recipient_user_id::text,
	project_id::text, session_id::text, source, type, title, body, dedupe_key,
	status, source_event_id, metadata, resolved_at, created_at, updated_at`

func scanNotification(row rowScanner, notification *domain.Notification) error {
	return row.Scan(notificationScanTargets(notification)...)
}

func notificationScanTargets(notification *domain.Notification) []any {
	return []any{
		&notification.ID, &notification.OrgID, &notification.RecipientUserID,
		&notification.ProjectID, &notification.SessionID, &notification.Source,
		&notification.Type, &notification.Title, &notification.Body,
		&notification.DedupeKey, &notification.Status, &notification.EventID,
		&notification.Metadata, &notification.ResolvedAt,
		&notification.CreatedAt, &notification.UpdatedAt,
	}
}
