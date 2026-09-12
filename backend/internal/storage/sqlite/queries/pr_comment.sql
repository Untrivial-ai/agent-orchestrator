-- name: UpsertPRComment :exec
INSERT INTO pr_comment (pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, review_id, url, is_bot, auto_inject_review)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pr_url, comment_id) DO UPDATE SET
    author = excluded.author,
    file = excluded.file,
    line = excluded.line,
    body = excluded.body,
    resolved = excluded.resolved,
    created_at = excluded.created_at,
    thread_id = excluded.thread_id,
    review_id = excluded.review_id,
    url = excluded.url,
    is_bot = excluded.is_bot;

-- name: InsertLegacyPRComment :exec
INSERT OR IGNORE INTO pr_comment (pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, review_id, url, is_bot)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteLegacyPRComments :exec
DELETE FROM pr_comment WHERE pr_url = ? AND thread_id = '';

-- name: DeletePRComment :exec
DELETE FROM pr_comment WHERE pr_url = ? AND comment_id = ?;

-- name: MarkPRCommentResolved :execrows
UPDATE pr_comment SET resolved = TRUE WHERE pr_url = ? AND comment_id = ?;

-- name: ListPRComments :many
SELECT pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, url, is_bot, auto_inject_review, review_id
FROM pr_comment WHERE pr_url = ? ORDER BY created_at, comment_id;
