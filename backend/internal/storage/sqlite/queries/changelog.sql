-- name: ReadChangeLogAfter :many
SELECT seq, project_id, session_id, event_type, payload, created_at
FROM change_log WHERE seq > ? ORDER BY seq LIMIT ?;


-- name: MaxChangeLogSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) AS seq FROM change_log;


-- name: PruneChangeLogBefore :execrows
DELETE FROM change_log
WHERE seq IN (
    SELECT cl.seq
    FROM change_log AS cl
    WHERE cl.created_at < ?
    ORDER BY cl.created_at ASC, cl.seq ASC
    LIMIT ?
);


-- name: PruneChangeLogToMaxRows :execrows
DELETE FROM change_log
WHERE seq IN (
    SELECT cl.seq
    FROM change_log AS cl
    ORDER BY cl.seq ASC
    LIMIT MIN(sqlc.arg(batch_limit), MAX(0, (SELECT COUNT(*) FROM change_log) - sqlc.arg(max_rows)))
);

-- NOTE: `DELETE FROM change_log WHERE session_id = ?` is intentionally NOT
-- a sqlc query. sqlc 1.31's SQLite parser strips the `?` placeholder and
-- emits a *domain.SessionID pointer parameter whenever a nullable column
-- (change_log.session_id is nullable for project-level events) sits on the
-- LHS of `=` in a top-level DELETE. None of the obvious SQL workarounds
-- (sqlc.arg, IFNULL, rowid subquery, second predicate) defeated the
-- heuristic. The store runs that DELETE directly via tx.ExecContext inside
-- Store.DeleteSession to keep it part of the same transaction as the seed
-- probe + session delete.
