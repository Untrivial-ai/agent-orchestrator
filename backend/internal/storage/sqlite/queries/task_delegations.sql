-- name: InsertTaskDelegation :execrows
INSERT INTO task_delegations (
    idempotency_key, request_fingerprint, state, created_at, updated_at
) VALUES (?, ?, 'pending', ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetTaskDelegation :one
SELECT idempotency_key, request_fingerprint, worker_id, state, created_at, updated_at
FROM task_delegations
WHERE idempotency_key = ?;

-- name: CompleteTaskDelegation :execrows
UPDATE task_delegations SET
    worker_id = sqlc.arg(worker_id),
    state = 'completed',
    updated_at = sqlc.arg(updated_at)
WHERE idempotency_key = sqlc.arg(idempotency_key)
  AND request_fingerprint = sqlc.arg(request_fingerprint)
  AND state = 'pending';
