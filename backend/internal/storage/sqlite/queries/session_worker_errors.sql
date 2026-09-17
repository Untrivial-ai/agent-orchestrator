-- Worker watchdog error log. One bounded row per observed worker failure;
-- the watchdog derives needsAttention at read time from these rows plus
-- activity recency. Retention is enforced by the writer (newest N per
-- session); errors for rolled-back turns are excluded at read time because
-- rollback discards that history provider-side.
--
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by
-- byte offset, so a multi-byte character here silently corrupts later queries.

-- name: RecordSessionWorkerError :exec
INSERT INTO session_worker_errors (
    session_id, source, turn_id, error_message, occurred_at
) VALUES (?, ?, ?, ?, ?);

-- name: CountSessionWorkerErrors :one
SELECT COUNT(*) FROM session_worker_errors WHERE session_id = ?;

-- name: DeleteOldestSessionWorkerErrors :execrows
DELETE FROM session_worker_errors
WHERE id IN (
    SELECT e.id
    FROM session_worker_errors AS e
    WHERE e.session_id = ?
    ORDER BY e.occurred_at ASC, e.id ASC
    LIMIT ?
);

-- name: SelectRecentSessionWorkerErrors :many
SELECT e.session_id, e.source, e.turn_id, e.error_message, e.occurred_at
FROM session_worker_errors AS e
LEFT JOIN conversation_turns AS t ON t.id = e.turn_id
WHERE e.session_id = ?
  AND (e.turn_id = '' OR t.rolled_back_at IS NULL)
ORDER BY e.occurred_at DESC, e.id DESC;

-- name: SelectRecentSessionWorkerErrorsForSessions :many
-- Batch form of SelectRecentSessionWorkerErrors for board/session-list reads.
-- The JSON array of session ids keeps the list path bounded instead of
-- issuing one error query per card. No per-session limit here: the writer
-- prunes every session to its newest rows, so one noisy session cannot crowd
-- out the others, and callers trim to their derivation window in Go.
WITH wanted_session AS (
    SELECT CAST(j.value AS TEXT) AS session_id
    FROM json_each(?) AS j
)
SELECT e.session_id, e.source, e.turn_id, e.error_message, e.occurred_at
FROM session_worker_errors AS e
JOIN wanted_session ON wanted_session.session_id = e.session_id
LEFT JOIN conversation_turns AS t ON t.id = e.turn_id
WHERE e.turn_id = '' OR t.rolled_back_at IS NULL
ORDER BY e.session_id, e.occurred_at DESC, e.id DESC;
