-- name: UpdateSessionFromActivitySignal :execrows
-- Lifecycle reads the session before reducing a hook. Fence the resulting
-- narrow write against a concurrent revision change so a stale reducer cannot
-- clobber a newer write.
UPDATE sessions SET
    activity_state = sqlc.arg(activity_state),
    activity_last_at = sqlc.arg(activity_last_at),
    first_signal_at = sqlc.arg(first_signal_at),
    agent_session_id = sqlc.arg(agent_session_id),
    agent_session_id_launch_id = sqlc.arg(agent_session_id_launch_id),
    native_identity_observed_at = sqlc.arg(native_identity_observed_at),
    latest_user_prompt = sqlc.arg(latest_user_prompt),
    latest_user_prompt_at = sqlc.arg(latest_user_prompt_at),
    latest_assistant_update = sqlc.arg(latest_assistant_update),
    latest_assistant_update_at = sqlc.arg(latest_assistant_update_at),
    conversation_checkpoint_state = sqlc.arg(conversation_checkpoint_state),
    conversation_checkpoint_generation = sqlc.arg(conversation_checkpoint_generation),
    conversation_checkpoint_native_id = sqlc.arg(conversation_checkpoint_native_id),
    conversation_checkpoint_unsettled = sqlc.arg(conversation_checkpoint_unsettled),
    conversation_checkpoint_turn_id = sqlc.arg(conversation_checkpoint_turn_id),
    native_checkpoint_evidence = sqlc.arg(native_checkpoint_evidence),
    native_transcript_path = sqlc.arg(native_transcript_path),
    updated_at = sqlc.arg(updated_at)
WHERE sessions.id = sqlc.arg(id)
  AND sessions.revision = sqlc.arg(expected_revision)
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
  );
