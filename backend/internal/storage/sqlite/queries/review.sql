-- name: UpsertReview :exec
INSERT INTO review (id, session_id, project_id, harness, pr_url, reviewer_handle_id, agent_session_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, harness) DO UPDATE SET
    project_id = excluded.project_id,
    pr_url = excluded.pr_url,
    reviewer_handle_id = excluded.reviewer_handle_id,
    agent_session_id = CASE WHEN excluded.agent_session_id != '' THEN excluded.agent_session_id ELSE review.agent_session_id END,
    updated_at = excluded.updated_at;

-- name: GetReviewBySession :one
SELECT id, session_id, project_id, harness, pr_url, reviewer_handle_id, agent_session_id, created_at, updated_at
FROM review WHERE session_id = ? ORDER BY updated_at DESC, created_at DESC, id DESC LIMIT 1;

-- name: GetReviewBySessionAndHarness :one
SELECT id, session_id, project_id, harness, pr_url, reviewer_handle_id, agent_session_id, created_at, updated_at
FROM review WHERE session_id = ? AND harness = ?;

-- name: GetReviewByID :one
SELECT id, session_id, project_id, harness, pr_url, reviewer_handle_id, agent_session_id, created_at, updated_at
FROM review WHERE id = ?;

-- name: ListReviewsBySession :many
SELECT id, session_id, project_id, harness, pr_url, reviewer_handle_id, agent_session_id, created_at, updated_at
FROM review WHERE session_id = ? ORDER BY updated_at DESC, created_at DESC, id DESC;

-- name: ClearReviewerHandle :exec
UPDATE review SET reviewer_handle_id = '', updated_at = CURRENT_TIMESTAMP WHERE session_id = ?;

-- name: ClearReviewerHandleByHarness :exec
UPDATE review SET reviewer_handle_id = '', updated_at = CURRENT_TIMESTAMP WHERE session_id = ? AND harness = ?;

-- name: UpdateReviewAgentSessionID :execrows
UPDATE review SET agent_session_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: InsertReviewRun :exec
INSERT INTO review_run (id, review_id, session_id, batch_id, harness, trigger_source, pr_url, target_sha, status, verdict, body, github_review_id, created_at, auto_inject_review)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateReviewRunResult :execrows
UPDATE review_run SET status = ?, verdict = ?, body = ?, github_review_id = ?, auto_inject_review = ? WHERE id = ? AND status = 'running';

-- name: SupersedeStaleRunningReviewRuns :execrows
UPDATE review_run SET status = 'failed', body = ? WHERE session_id = ? AND pr_url = ? AND target_sha != ? AND status = 'running' AND verdict = '';

-- name: CancelRunningReviewRunsBySession :execrows
UPDATE review_run SET status = 'cancelled', body = ? WHERE session_id = ? AND status = 'running' AND verdict = '';

-- name: CancelRunningReviewRunsBySessionAndHarness :execrows
UPDATE review_run SET status = 'cancelled', body = ? WHERE session_id = ? AND harness = ? AND status = 'running' AND verdict = '';

-- name: MarkReviewRunDelivered :execrows
UPDATE review_run SET status = 'delivered', delivered_at = ? WHERE id = ? AND status = 'complete' AND delivered_at IS NULL;

-- name: GetReviewRun :one
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE id = ?;

-- name: GetReviewRunBySessionPRAndSHA :one
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE session_id = ? AND pr_url = ? AND target_sha = ? ORDER BY created_at DESC LIMIT 1;

-- name: GetReviewRunBySessionPRSHAAndHarness :one
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE session_id = ? AND pr_url = ? AND target_sha = ? AND harness = ? ORDER BY created_at DESC LIMIT 1;

-- name: ListReviewRunsBySession :many
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE session_id = ? ORDER BY created_at DESC;

-- name: ListRunningReviewRunsBySession :many
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE session_id = ? AND status = 'running' AND verdict = '' ORDER BY created_at DESC;

-- name: ListReviewRunsByBatch :many
SELECT id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at, github_review_id, delivered_at, batch_id, auto_inject_review, trigger_source
FROM review_run WHERE session_id = ? AND batch_id = ? ORDER BY created_at ASC, id ASC;
