-- name: CreateNotification :one
INSERT INTO notifications (
    id, session_id, project_id, pr_url, event_key, type, title, body, status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListUnreadNotificationsPage :many
SELECT *
FROM notifications
WHERE status = 'unread'
  AND dismissed_at IS NULL
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- Unresolved is the still-actionable set: the underlying issue has not gone
-- away yet. Terminal facts (turn outcomes, pr_merged, pr_closed_unmerged)
-- describe something that already happened, so they are unseen-only and never
-- listed here.
-- name: ListUnresolvedNotificationsPage :many
SELECT *
FROM notifications
WHERE resolved_at IS NULL
  AND dismissed_at IS NULL
  AND type IN ('needs_input', 'ci_failed', 'ready_to_merge')
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListNotificationsPage :many
SELECT *
FROM notifications
WHERE dismissed_at IS NULL
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: CountUnreadNotifications :one
SELECT COUNT(*)
FROM notifications
WHERE status = 'unread'
  AND dismissed_at IS NULL;

-- name: CountUnresolvedNotifications :one
SELECT COUNT(*)
FROM notifications
WHERE resolved_at IS NULL
  AND dismissed_at IS NULL
  AND type IN ('needs_input', 'ci_failed', 'ready_to_merge');

-- name: MarkNotificationRead :one
UPDATE notifications
SET status = 'read'
WHERE id = ?
  AND status = 'unread'
  AND dismissed_at IS NULL
RETURNING *;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications
SET status = 'read'
WHERE status = 'unread'
  AND dismissed_at IS NULL;

-- name: ResolveSessionNotificationsByType :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE session_id = sqlc.arg(session_id)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
RETURNING *;

-- Dismissed rows are dedupe tombstones, not visible history. Once the issue
-- resolves they have no remaining purpose and must not emit resolved events.
-- name: DeleteDismissedSessionNotificationsByType :execrows
DELETE FROM notifications
WHERE session_id = sqlc.arg(session_id)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL;

-- name: ResolvePRNotificationsByType :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE pr_url = sqlc.arg(pr_url)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
RETURNING *;

-- name: DeleteDismissedPRNotificationsByType :execrows
DELETE FROM notifications
WHERE pr_url = sqlc.arg(pr_url)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL;

-- Readiness is more than open/closed: draft, CI, review decision, unresolved
-- human comments, and mergeability all block a merge. Rather than restate that
-- rule in SQL and let it drift from the live path, this returns the open rows
-- and lets domain.MergeReadiness judge them against the stored PR facts.
-- name: ListOpenReadyToMergeNotifications :many
SELECT *
FROM notifications
WHERE type = 'ready_to_merge'
  AND resolved_at IS NULL;

-- name: ListOpenCIFailedNotifications :many
SELECT *
FROM notifications
WHERE type = 'ci_failed'
  AND resolved_at IS NULL;

-- Restart reconciliation: a resolution transition observed while the daemon was
-- down never reaches lifecycle, so open rows are re-checked against the durable
-- session/PR facts on startup.
-- name: ResolveStaleNeedsInputNotifications :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE type = 'needs_input'
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
  AND session_id IN (
    SELECT id FROM sessions
    WHERE is_terminated = TRUE
       OR activity_state NOT IN ('waiting_input', 'blocked')
  )
RETURNING *;

-- name: DeleteStaleDismissedNeedsInputNotifications :execrows
DELETE FROM notifications
WHERE type = 'needs_input'
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL
  AND session_id IN (
    SELECT id FROM sessions
    WHERE is_terminated = TRUE
       OR activity_state NOT IN ('waiting_input', 'blocked')
  );

-- name: GetOpenNotificationByDedupe :one
SELECT *
FROM notifications
WHERE session_id = ?
  AND type = ?
  AND pr_url = ?
  AND event_key = ?
  AND (status = 'unread' OR resolved_at IS NULL)
LIMIT 1;

-- name: ClearAllNotifications :execrows
UPDATE notifications
SET dismissed_at = CURRENT_TIMESTAMP,
    status = 'read'
WHERE dismissed_at IS NULL;

-- Resolved notifications no longer carry a live dedupe fact. Remove them in
-- the same transaction that dismisses visible history so only open tombstones
-- remain.
-- name: DeleteResolvedDismissedNotifications :execrows
DELETE FROM notifications
WHERE dismissed_at IS NOT NULL
  AND resolved_at IS NOT NULL;

-- Read before dismissal so the delete response and live event keep the row's
-- original unread state. Clients use that state to decrement their badge.
-- name: GetNotificationForDismissal :one
SELECT *
FROM notifications
WHERE id = ?
  AND dismissed_at IS NULL;

-- name: DismissNotification :execrows
UPDATE notifications
SET dismissed_at = CURRENT_TIMESTAMP,
    status = 'read'
WHERE id = ?
  AND dismissed_at IS NULL;
