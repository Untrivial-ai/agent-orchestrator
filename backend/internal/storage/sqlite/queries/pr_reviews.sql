-- name: UpsertPRReview :exec
INSERT INTO pr_reviews (pr_url, review_id, author, state, url, is_bot, submitted_at, body, target_sha, auto_inject_review)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pr_url, review_id) DO UPDATE SET
    author = excluded.author,
    state = excluded.state,
    url = excluded.url,
    is_bot = excluded.is_bot,
    submitted_at = excluded.submitted_at,
    body = excluded.body,
    target_sha = excluded.target_sha;

-- name: DeletePRReviews :exec
DELETE FROM pr_reviews WHERE pr_url = ?;

-- name: DeletePRReview :exec
DELETE FROM pr_reviews WHERE pr_url = ? AND review_id = ?;

-- name: ListPRReviews :many
SELECT pr_url, review_id, author, state, url, is_bot, submitted_at, body, auto_inject_review, target_sha
FROM pr_reviews WHERE pr_url = ? ORDER BY submitted_at, review_id;
