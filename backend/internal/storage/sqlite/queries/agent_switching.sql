-- name: InsertAgentNativeSession :execrows
INSERT INTO agent_native_sessions (
    id, ao_session_id, harness, config_dir,
    native_session_id, transcript_path,
    last_generation_id, created_at, last_used_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetAgentNativeSession :one
SELECT id, ao_session_id, harness, config_dir,
    native_session_id, transcript_path,
    last_generation_id, created_at, last_used_at
FROM agent_native_sessions
WHERE id = ?;

-- name: FindAgentNativeSession :one
SELECT id, ao_session_id, harness, config_dir,
    native_session_id, transcript_path,
    last_generation_id, created_at, last_used_at
FROM agent_native_sessions
WHERE ao_session_id = ?
  AND harness = ?
  AND config_dir = ?
  AND native_session_id = ?;

-- name: ListAgentNativeSessions :many
SELECT id, ao_session_id, harness, config_dir,
    native_session_id, transcript_path,
    last_generation_id, created_at, last_used_at
FROM agent_native_sessions
WHERE ao_session_id = ?
ORDER BY last_used_at DESC, created_at DESC, id DESC;

-- name: UpdateAgentNativeSession :execrows
UPDATE agent_native_sessions SET
    config_dir = sqlc.arg(config_dir),
    native_session_id = sqlc.arg(native_session_id),
    transcript_path = sqlc.arg(transcript_path),
    last_generation_id = sqlc.arg(next_generation_id),
    last_used_at = sqlc.arg(last_used_at)
WHERE id = sqlc.arg(id)
  AND ao_session_id = sqlc.arg(ao_session_id)
  AND last_generation_id = sqlc.arg(expected_generation_id);

-- name: InsertAgentSwitch :execrows
INSERT INTO agent_switches (
    id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness,
    target_native_session_ref, target_start_mode,
    state, agent_handoff_status, source_transcript_status, semantic_handoff_included,
    agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id,
    target_acknowledged_at, error_code,
    requested_at, updated_at,
    final_handoff_path, final_handoff_hash
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT DO NOTHING;

-- name: GetAgentSwitch :one
SELECT id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness,
    target_native_session_ref, target_start_mode,
    state, agent_handoff_status, source_transcript_status, semantic_handoff_included,
    agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id,
    target_acknowledged_at, error_code,
    requested_at, updated_at,
    final_handoff_path, final_handoff_hash
FROM agent_switches
WHERE id = ?;

-- name: GetAgentSwitchByIdempotencyKey :one
SELECT id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness,
    target_native_session_ref, target_start_mode,
    state, agent_handoff_status, source_transcript_status, semantic_handoff_included,
    agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id,
    target_acknowledged_at, error_code,
    requested_at, updated_at,
    final_handoff_path, final_handoff_hash
FROM agent_switches
WHERE session_id = ? AND idempotency_key = ?;

-- name: GetActiveAgentSwitch :one
SELECT id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness,
    target_native_session_ref, target_start_mode,
    state, agent_handoff_status, source_transcript_status, semantic_handoff_included,
    agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id,
    target_acknowledged_at, error_code,
    requested_at, updated_at,
    final_handoff_path, final_handoff_hash
FROM agent_switches
WHERE session_id = ?
  AND state NOT IN ('completed', 'failed');

-- name: ListActiveAgentSwitches :many
SELECT id, session_id, idempotency_key, request_fingerprint,
       from_harness, target_harness,
       target_native_session_ref, target_start_mode,
       state, agent_handoff_status, source_transcript_status,
       semantic_handoff_included,
       agent_handoff_path, agent_handoff_hash,
       source_generation_id, target_generation_id,
       target_runtime_handle_id, target_acknowledged_at,
       error_code, requested_at, updated_at,
       final_handoff_path, final_handoff_hash
FROM agent_switches
WHERE state NOT IN ('completed', 'failed');

-- name: ListAgentSwitches :many
SELECT id, session_id, idempotency_key, request_fingerprint,
    from_harness, target_harness,
    target_native_session_ref, target_start_mode,
    state, agent_handoff_status, source_transcript_status, semantic_handoff_included,
    agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id,
    target_acknowledged_at, error_code,
    requested_at, updated_at,
    final_handoff_path, final_handoff_hash
FROM agent_switches
WHERE session_id = ?
ORDER BY requested_at DESC, id DESC;

-- name: UpdateAgentSwitch :execrows
UPDATE agent_switches SET
    target_native_session_ref = sqlc.narg(target_native_session_ref),
    target_start_mode = sqlc.arg(target_start_mode),
    state = sqlc.arg(next_state),
    target_generation_id = sqlc.arg(next_target_generation_id),
    target_runtime_handle_id = sqlc.arg(next_target_runtime_handle_id),
    error_code = sqlc.arg(error_code),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = sqlc.arg(expected_state)
  AND source_generation_id = sqlc.arg(expected_source_generation_id)
  AND target_generation_id = sqlc.arg(expected_target_generation_id)
  AND (
      error_code = ''
      OR error_code = sqlc.arg(error_code)
      OR (
		  error_code IN ('source_stop_unconfirmed', 'source_restore_unconfirmed')
          AND sqlc.arg(next_state) = 'failed'
          AND sqlc.arg(error_code) <> ''
		  AND sqlc.arg(error_code) <> error_code
      )
  )
  AND (
      target_runtime_handle_id = ''
      OR target_runtime_handle_id = sqlc.arg(next_target_runtime_handle_id)
  );

-- name: FailAgentSwitchIfUnacknowledged :execrows
UPDATE agent_switches SET
    state = 'failed',
    error_code = sqlc.arg(error_code),
    updated_at = sqlc.arg(failed_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = 'delivering_context'
  AND source_generation_id = sqlc.arg(expected_source_generation_id)
  AND target_generation_id = sqlc.arg(expected_target_generation_id)
  AND target_generation_id <> ''
  AND target_acknowledged_at IS NULL
  AND updated_at <= sqlc.arg(failed_at);

-- name: RequestAgentHandoff :execrows
UPDATE agent_switches SET
    agent_handoff_status = 'requested',
    agent_handoff_path = '',
    agent_handoff_hash = '',
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND source_generation_id = sqlc.arg(source_generation_id)
  AND state = 'preparing_handoff'
  AND agent_handoff_status = 'not_attempted';

-- name: MarkAgentHandoffUnavailable :execrows
UPDATE agent_switches SET
    agent_handoff_status = 'unavailable',
    agent_handoff_path = '',
    agent_handoff_hash = '',
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND source_generation_id = sqlc.arg(source_generation_id)
  AND state IN (
      'preparing_handoff', 'stopping_source',
      'source_stopped', 'starting_target', 'target_ready', 'delivering_context'
  )
  AND agent_handoff_status IN ('not_attempted', 'requested');

-- name: SettleAgentHandoff :execrows
UPDATE agent_switches SET
    agent_handoff_status = sqlc.arg(next_agent_handoff_status),
    agent_handoff_path = sqlc.arg(agent_handoff_path),
    agent_handoff_hash = sqlc.arg(agent_handoff_hash),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND source_generation_id = sqlc.arg(source_generation_id)
  AND state = 'preparing_handoff'
  AND agent_handoff_status = 'requested';

-- name: FinalizeAgentSwitchHandoff :execrows
UPDATE agent_switches SET
    final_handoff_path = sqlc.arg(final_handoff_path),
    final_handoff_hash = sqlc.arg(final_handoff_hash),
    source_transcript_status = sqlc.arg(source_transcript_status),
    semantic_handoff_included = sqlc.arg(semantic_included),
    agent_handoff_path = CASE
        WHEN agent_handoff_status = 'received' AND CAST(sqlc.arg(semantic_included) AS INTEGER) = 1 THEN sqlc.arg(final_handoff_path)
        ELSE agent_handoff_path
    END,
    agent_handoff_hash = CASE
        WHEN agent_handoff_status = 'received' AND CAST(sqlc.arg(semantic_included) AS INTEGER) = 1 THEN sqlc.arg(final_handoff_hash)
        ELSE agent_handoff_hash
    END,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = 'source_stopped'
  AND source_generation_id = sqlc.arg(source_generation_id)
  AND target_generation_id = sqlc.arg(target_generation_id)
  AND target_generation_id <> ''
  AND final_handoff_path = ''
  AND final_handoff_hash = ''
  AND source_transcript_status = 'not_attempted'
  AND (
      CAST(sqlc.arg(semantic_included) AS INTEGER) = 0
      OR agent_handoff_status = 'received'
  );

-- name: AcknowledgeAgentSwitchTarget :execrows
UPDATE agent_switches SET
    target_acknowledged_at = sqlc.arg(target_acknowledged_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = 'delivering_context'
  AND target_generation_id = sqlc.arg(target_generation_id)
  AND target_generation_id <> ''
  AND updated_at <= sqlc.arg(updated_at)
  AND target_acknowledged_at IS NULL;

-- name: UpdateSessionFromActivitySignal :execrows
-- Lifecycle reads the session before reducing a hook. Fence the resulting
-- narrow write against a concurrent ownership transfer, and reject every
-- callback while the still-projected owner is the source being torn down.
-- Target hooks remain allowed after atomic activation moves both the session
-- owner/generation and switch state out of these source-side phases.
UPDATE sessions SET
    activity_state = sqlc.arg(activity_state),
    activity_last_at = sqlc.arg(activity_last_at),
    first_signal_at = sqlc.arg(first_signal_at),
    agent_session_id = sqlc.arg(agent_session_id),
    agent_session_id_launch_id = sqlc.arg(agent_session_id_launch_id),
    latest_user_prompt = sqlc.arg(latest_user_prompt),
    latest_assistant_update = sqlc.arg(latest_assistant_update),
    native_transcript_path = sqlc.arg(native_transcript_path),
    updated_at = sqlc.arg(updated_at)
WHERE sessions.id = sqlc.arg(id)
  AND sessions.is_terminated = 0
  AND sessions.harness = sqlc.arg(expected_harness)
  AND sessions.session_mode = sqlc.arg(expected_session_mode)
  AND (
      (
          sqlc.arg(expected_session_mode) <> 'chat'
          AND sessions.runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
      )
      OR
      (
          sqlc.arg(expected_session_mode) = 'chat'
          AND sessions.controller_generation = sqlc.arg(expected_controller_generation)
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM agent_switches AS active_switch
      WHERE active_switch.session_id = sessions.id
        AND active_switch.from_harness = sessions.harness
        AND active_switch.state IN (
            'stopping_source', 'source_stopped', 'starting_target'
        )
  );

-- name: MarkSessionAgentSwitchSourceStopped :execrows
UPDATE sessions SET
    activity_state = 'exited',
    activity_last_at = sqlc.arg(stopped_at),
    updated_at = sqlc.arg(stopped_at)
WHERE id = sqlc.arg(session_id)
  AND is_terminated = 0
  AND harness = sqlc.arg(expected_source_harness)
  AND runtime_launch_id = sqlc.arg(expected_source_runtime_launch_id)
  AND activity_last_at <= sqlc.arg(stopped_at);

-- name: MarkAgentSwitchSourceStopped :execrows
UPDATE agent_switches SET
    state = 'source_stopped',
	error_code = '',
    updated_at = sqlc.arg(stopped_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = 'stopping_source'
  AND from_harness = sqlc.arg(expected_source_harness)
  AND source_generation_id = sqlc.arg(expected_source_generation_id)
  AND target_generation_id = sqlc.arg(expected_target_generation_id)
  AND target_generation_id <> ''
  AND updated_at <= sqlc.arg(stopped_at);

-- name: ActivateSessionAgentSwitchTarget :execrows
UPDATE sessions SET
    harness = sqlc.arg(target_harness),
    activity_state = 'idle',
    activity_last_at = sqlc.arg(activated_at),
    first_signal_at = NULL,
    runtime_handle_id = sqlc.arg(runtime_handle_id),
    runtime_launch_id = sqlc.arg(target_generation_id),
    agent_session_id = sqlc.arg(target_native_session_id),
    agent_session_id_launch_id = sqlc.arg(target_generation_id),
    native_transcript_path = sqlc.arg(target_native_transcript_path),
    updated_at = sqlc.arg(activated_at)
WHERE id = sqlc.arg(session_id)
  AND is_terminated = 0
  AND activity_state = 'exited'
  AND harness = sqlc.arg(expected_source_harness)
  AND runtime_launch_id = sqlc.arg(expected_source_runtime_launch_id)
  AND activity_last_at <= sqlc.arg(activated_at);

-- name: MarkAgentSwitchTargetReady :execrows
UPDATE agent_switches SET
    state = 'target_ready',
    updated_at = sqlc.arg(activated_at)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND state = 'starting_target'
  AND from_harness = sqlc.arg(expected_source_harness)
  AND target_harness = sqlc.arg(expected_target_harness)
  AND source_generation_id = sqlc.arg(expected_source_generation_id)
  AND target_generation_id = sqlc.arg(expected_target_generation_id)
  AND target_native_session_ref = sqlc.arg(expected_target_native_session_ref)
  AND error_code = ''
  AND (
      target_runtime_handle_id = ''
      OR target_runtime_handle_id = sqlc.arg(expected_target_runtime_handle_id)
  )
  AND updated_at <= sqlc.arg(activated_at)
  AND target_acknowledged_at IS NULL;
