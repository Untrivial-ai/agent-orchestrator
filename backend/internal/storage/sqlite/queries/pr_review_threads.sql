-- Summary: SQLC queries for replacing and reading normalized PR review threads.
-- name: UpsertPRReviewThread :exec
INSERT INTO pr_review_threads (pr_url, thread_id, path, line, resolved, is_bot, semantic_hash, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pr_url, thread_id) DO UPDATE SET
    path = excluded.path,
    line = excluded.line,
    resolved = excluded.resolved,
    is_bot = excluded.is_bot,
    semantic_hash = excluded.semantic_hash,
    updated_at = excluded.updated_at;

-- name: DeletePRReviewThreads :exec
DELETE FROM pr_review_threads WHERE pr_url = ?;

-- name: DeletePRReviewThread :exec
DELETE FROM pr_review_threads WHERE pr_url = ? AND thread_id = ?;

-- name: ListPRReviewThreads :many
SELECT pr_url, thread_id, path, line, resolved, is_bot, semantic_hash, updated_at
FROM pr_review_threads WHERE pr_url = ? ORDER BY updated_at, thread_id;

-- name: MarkPRReviewThreadResolved :execrows
UPDATE pr_review_threads
SET resolved = 1
WHERE pr_url = ? AND thread_id = ? AND resolved = 0;
