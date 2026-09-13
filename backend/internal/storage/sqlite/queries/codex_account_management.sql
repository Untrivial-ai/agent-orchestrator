-- name: GetCodexActiveAccount :one
SELECT account_id, revision, activated_at, updated_at
FROM codex_active_account WHERE singleton_id = 1;

-- name: InsertCodexActiveAccount :execrows
INSERT INTO codex_active_account (singleton_id, account_id, revision, activated_at, updated_at)
VALUES (1, sqlc.arg(account_id), 1, sqlc.arg(activated_at), sqlc.arg(updated_at))
ON CONFLICT DO NOTHING;

-- name: UpdateCodexActiveAccount :execrows
UPDATE codex_active_account
SET account_id = sqlc.arg(account_id),
    revision = revision + 1,
    activated_at = sqlc.arg(activated_at),
    updated_at = sqlc.arg(updated_at)
WHERE singleton_id = 1 AND revision = sqlc.arg(expected_revision);

-- name: InsertCodexAccountSwitch :execrows
INSERT INTO codex_account_switches (
	 id, source_kind, source_account_id, target_account_id, idempotency_key,
	 request_fingerprint, expected_account_revision, restart_running_sessions, phase, failure_code,
	 created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, ?, '', ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetCodexAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   request_fingerprint, expected_account_revision, phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, restart_running_sessions, source_kind
FROM codex_account_switches WHERE id = ?;

-- name: GetCodexAccountSwitchByIdempotency :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   request_fingerprint, expected_account_revision, phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, restart_running_sessions, source_kind
FROM codex_account_switches WHERE idempotency_key = ?;

-- name: GetActiveCodexAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   request_fingerprint, expected_account_revision, phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, restart_running_sessions, source_kind
FROM codex_account_switches
WHERE phase NOT IN ('completed', 'failed')
ORDER BY created_at LIMIT 1;

-- name: UpdateCodexAccountSwitchPhase :execrows
UPDATE codex_account_switches
SET phase = sqlc.arg(next_phase), failure_code = sqlc.arg(failure_code),
    credentials_committed_at = sqlc.narg(credentials_committed_at),
    updated_at = sqlc.arg(updated_at), completed_at = sqlc.narg(completed_at)
WHERE id = sqlc.arg(id) AND phase = sqlc.arg(expected_phase);
