-- name: InsertAutomation :exec
INSERT INTO automations (
    id, project_id, display_name, prompt, kind, harness, rrule_text,
    timezone, enabled, next_run_at, last_run_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAutomation :one
SELECT id, project_id, display_name, prompt, kind, harness, rrule_text,
    timezone, enabled, next_run_at, last_run_at, created_at, updated_at
FROM automations
WHERE id = ?;

-- name: ListAutomations :many
SELECT id, project_id, display_name, prompt, kind, harness, rrule_text,
    timezone, enabled, next_run_at, last_run_at, created_at, updated_at
FROM automations
WHERE (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
  AND (sqlc.arg(enabled_filter) < 0 OR enabled = sqlc.arg(enabled_filter))
ORDER BY created_at, id
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountAutomations :one
SELECT COUNT(*)
FROM automations
WHERE (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
  AND (sqlc.arg(enabled_filter) < 0 OR enabled = sqlc.arg(enabled_filter));

-- name: UpdateAutomation :execrows
UPDATE automations
SET display_name = ?, prompt = ?, kind = ?, harness = ?, rrule_text = ?,
    timezone = ?, enabled = ?, next_run_at = ?, last_run_at = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAutomation :execrows
DELETE FROM automations WHERE id = ?;

-- name: AdvanceAutomationSchedule :execrows
UPDATE automations
SET last_run_at = ?, next_run_at = ?, updated_at = ?
WHERE id = ? AND enabled = 1 AND next_run_at = ?;

-- name: ListDueAutomations :many
SELECT id, project_id, display_name, prompt, kind, harness, rrule_text,
    timezone, enabled, next_run_at, last_run_at, created_at, updated_at
FROM automations
WHERE enabled = 1 AND next_run_at <= ?
ORDER BY next_run_at, id
LIMIT ?;

-- name: InsertAutomationRun :execrows
INSERT INTO automation_runs (
    id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (automation_id, scheduled_for) DO NOTHING;

-- name: GetAutomationRun :one
SELECT id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
FROM automation_runs
WHERE id = ?;

-- name: ListActiveAutomationRuns :many
SELECT id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
FROM automation_runs
WHERE status IN ('spawning', 'running')
ORDER BY scheduled_for, id;

-- name: ListExpiredSpawningAutomationRuns :many
SELECT id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
FROM automation_runs
WHERE status = 'spawning' AND lease_expires_at <= ?
ORDER BY scheduled_for, id;

-- name: GetAutomationRunByOccurrence :one
SELECT id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
FROM automation_runs
WHERE automation_id = ? AND scheduled_for = ?;

-- name: ListAutomationRuns :many
SELECT id, automation_id, scheduled_for, session_id, status, attempt_count,
    claimed_at, lease_expires_at, started_at, finished_at, error_message,
    created_at, updated_at
FROM automation_runs
WHERE automation_id = sqlc.arg(automation_id)
  AND (sqlc.arg(status_filter) = '' OR status = sqlc.arg(status_filter))
ORDER BY scheduled_for DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountAutomationRuns :one
SELECT COUNT(*)
FROM automation_runs
WHERE automation_id = sqlc.arg(automation_id)
  AND (sqlc.arg(status_filter) = '' OR status = sqlc.arg(status_filter));

-- name: ListLatestAutomationRuns :many
SELECT r.id, r.automation_id, r.scheduled_for, r.session_id, r.status,
    r.attempt_count, r.claimed_at, r.lease_expires_at, r.started_at,
    r.finished_at, r.error_message, r.created_at, r.updated_at
FROM automation_runs r
WHERE r.automation_id IN (sqlc.slice('automation_ids'))
  AND r.id = (
      SELECT newest.id
      FROM automation_runs newest
      WHERE newest.automation_id = r.automation_id
      ORDER BY newest.scheduled_for DESC, newest.id DESC
      LIMIT 1
  )
ORDER BY r.automation_id;

-- name: GetNextClaimableAutomationRun :one
SELECT r.id, r.automation_id, r.scheduled_for, r.session_id, r.status,
    r.attempt_count, r.claimed_at, r.lease_expires_at, r.started_at,
    r.finished_at, r.error_message, r.created_at, r.updated_at
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
WHERE r.status = 'pending'
  AND a.enabled = 1
  AND NOT EXISTS (
      SELECT 1 FROM automation_runs active
      WHERE active.automation_id = r.automation_id
        AND active.status IN ('spawning', 'running')
  )
ORDER BY r.scheduled_for, r.id
LIMIT 1;

-- name: ClaimAutomationRun :execrows
UPDATE automation_runs
SET status = 'spawning', attempt_count = attempt_count + 1,
    claimed_at = ?, lease_expires_at = ?, error_message = NULL, updated_at = ?
WHERE automation_runs.id = ? AND automation_runs.status = 'pending'
  AND NOT EXISTS (
      SELECT 1 FROM automation_runs active
      WHERE active.automation_id = automation_runs.automation_id
        AND active.id <> automation_runs.id
        AND active.status IN ('spawning', 'running')
  );

-- name: MarkAutomationRunRunning :execrows
UPDATE automation_runs
SET status = 'running', session_id = ?, started_at = ?,
    lease_expires_at = NULL, error_message = NULL, updated_at = ?
WHERE id = ? AND status = 'spawning';

-- name: CompleteAutomationRun :execrows
UPDATE automation_runs
SET status = 'completed', finished_at = ?, claimed_at = NULL,
    lease_expires_at = NULL, error_message = NULL, updated_at = ?
WHERE id = ? AND status = 'running';

-- name: FailAutomationRun :execrows
UPDATE automation_runs
SET status = 'failed', finished_at = ?, claimed_at = NULL,
    lease_expires_at = NULL, error_message = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'spawning', 'running');

-- name: ReleaseAutomationRun :execrows
UPDATE automation_runs
SET status = 'pending', claimed_at = NULL, lease_expires_at = NULL,
    error_message = ?, updated_at = ?
WHERE id = ? AND status = 'spawning';
