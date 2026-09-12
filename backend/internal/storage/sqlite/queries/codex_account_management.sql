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
    id, source_account_id, target_account_id, idempotency_key,
    request_fingerprint, expected_account_revision, phase, failure_code,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetCodexAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
       request_fingerprint, expected_account_revision, phase, failure_code,
       credentials_committed_at, created_at, updated_at, completed_at
FROM codex_account_switches WHERE id = ?;

-- name: GetCodexAccountSwitchByIdempotency :one
SELECT id, source_account_id, target_account_id, idempotency_key,
       request_fingerprint, expected_account_revision, phase, failure_code,
       credentials_committed_at, created_at, updated_at, completed_at
FROM codex_account_switches WHERE idempotency_key = ?;

-- name: GetActiveCodexAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
       request_fingerprint, expected_account_revision, phase, failure_code,
       credentials_committed_at, created_at, updated_at, completed_at
FROM codex_account_switches
WHERE phase NOT IN ('completed', 'failed')
ORDER BY created_at LIMIT 1;

-- name: UpdateCodexAccountSwitchPhase :execrows
UPDATE codex_account_switches
SET phase = sqlc.arg(next_phase), failure_code = sqlc.arg(failure_code),
    credentials_committed_at = sqlc.narg(credentials_committed_at),
    updated_at = sqlc.arg(updated_at), completed_at = sqlc.narg(completed_at)
WHERE id = sqlc.arg(id) AND phase = sqlc.arg(expected_phase);

-- name: InsertCodexAccountSwitchSession :execrows
INSERT INTO codex_account_switch_sessions (
    switch_id, session_id, native_session_id, interface_mode, source_handle_id, source_generation,
    was_running, stop_state, restart_state, reviewer_was_running,
    reviewer_source_handle_id, reviewer_native_session_id, reviewer_stop_state, reviewer_restart_state
) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListCodexAccountSwitchSessions :many
SELECT switch_id, session_id, native_session_id, interface_mode,
       source_handle_id, source_generation, was_running, stop_state, restart_state,
       reviewer_was_running, reviewer_source_handle_id, reviewer_native_session_id, reviewer_stop_state,
       reviewer_restart_state, error_code, stopped_at, restarted_at
FROM codex_account_switch_sessions WHERE switch_id = ? ORDER BY session_id;

-- name: UpdateCodexAccountSwitchSession :execrows
UPDATE codex_account_switch_sessions
SET stop_state = sqlc.arg(stop_state), restart_state = sqlc.arg(restart_state),
    error_code = sqlc.arg(error_code),
    reviewer_stop_state = sqlc.arg(reviewer_stop_state),
    reviewer_restart_state = sqlc.arg(reviewer_restart_state),
    stopped_at = sqlc.narg(stopped_at), restarted_at = sqlc.narg(restarted_at)
WHERE switch_id = sqlc.arg(switch_id) AND session_id = sqlc.arg(session_id)
  AND stop_state = sqlc.arg(expected_stop_state)
  AND restart_state = sqlc.arg(expected_restart_state);
