-- name: InsertTaskDelegation :execrows
INSERT INTO task_delegations (
    idempotency_key, request_fingerprint, state, created_at, updated_at, recoverable, startup_state
) VALUES (?, ?, 'pending', ?, ?, 1, 'seeded')
ON CONFLICT DO NOTHING;

-- name: GetTaskDelegation :one
SELECT idempotency_key, request_fingerprint, worker_id, state, created_at, updated_at, recoverable, startup_state
FROM task_delegations
WHERE idempotency_key = ?;

-- name: TaskDelegationStartupForWorker :one
SELECT startup_state FROM task_delegations WHERE worker_id = ?;

-- name: CompleteTaskDelegation :execrows
UPDATE task_delegations SET
    worker_id = sqlc.arg(worker_id),
    state = 'completed',
    startup_state = 'ready',
    updated_at = sqlc.arg(updated_at)
WHERE idempotency_key = sqlc.arg(idempotency_key)
  AND request_fingerprint = sqlc.arg(request_fingerprint)
  AND (state = 'pending' OR (worker_id = sqlc.arg(worker_id) AND startup_state = 'starting'));

-- name: BindTaskDelegationWorker :execrows
UPDATE task_delegations SET worker_id = ?, state = 'completed', updated_at = ?
WHERE idempotency_key = ? AND request_fingerprint = ?
  AND state = 'pending';

-- name: ClaimTaskDelegationStartup :execrows
UPDATE task_delegations SET startup_state = 'starting'
WHERE idempotency_key = ? AND request_fingerprint = ? AND worker_id = ?
  AND startup_state = 'seeded';
