-- name: NextSessionNum :one
SELECT COALESCE(MAX(num), 0) + 1 AS next FROM sessions WHERE project_id = ?;

-- name: InsertSession :exec
INSERT INTO sessions (
    id, project_id, num, issue_id, kind, harness, reviewer_harness, reviewer_agent_config, auto_review_enabled, display_name,
    activity_state, activity_last_at, first_signal_at, is_terminated,
    branch, workspace_path, workspace_repo_path, diff_base_sha, diff_base_ref, runtime_handle_id,
    runtime_launch_id, agent_session_id, agent_session_id_launch_id, prompt,
    latest_user_prompt, latest_user_prompt_at, latest_assistant_update, native_transcript_path,
    preview_url, preview_revision, terminate_on_pr_merge, cleanup_generation, browser_capability_verifier,
    session_mode, provider_conversation_id, controller_generation, model,
    created_at, updated_at, is_pinned, pinned_at, auto_inject_review, auto_inject_ci
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: UpdateSession :exec
UPDATE sessions SET
    issue_id = ?, kind = ?, harness = ?, reviewer_harness = ?, reviewer_agent_config = ?, auto_review_enabled = ?, display_name = ?,
    activity_state = ?, activity_last_at = ?, first_signal_at = ?, is_terminated = ?,
    branch = ?, workspace_path = ?, workspace_repo_path = ?, diff_base_sha = ?, diff_base_ref = ?, runtime_handle_id = ?,
    runtime_launch_id = ?, agent_session_id = ?, agent_session_id_launch_id = ?, prompt = ?,
    latest_user_prompt = ?, latest_user_prompt_at = ?, latest_assistant_update = ?, native_transcript_path = ?,
    preview_url = ?, preview_revision = ?, terminate_on_pr_merge = ?,
    cleanup_generation = ?, browser_capability_verifier = ?,
    provider_conversation_id = ?, controller_generation = ?, model = ?, updated_at = ?,
    is_pinned = ?, pinned_at = ?, auto_inject_review = ?, auto_inject_ci = ?
WHERE id = ?;

-- name: UpdateBrowserCapabilityVerifier :execrows
-- Rotate only the browser credential for the exact controller owner observed by
-- the launcher. This must not replay a stale SessionRecord over newer lifecycle,
-- activity, termination, or provider ownership facts.
UPDATE sessions SET
    browser_capability_verifier = sqlc.arg(browser_capability_verifier),
    updated_at = MAX(updated_at, sqlc.arg(updated_at))
WHERE id = sqlc.arg(id)
  AND harness = sqlc.arg(expected_harness)
  AND session_mode = sqlc.arg(expected_session_mode)
  AND is_terminated = sqlc.arg(expected_is_terminated)
  AND runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
  AND agent_session_id = sqlc.arg(expected_agent_session_id)
  AND agent_session_id_launch_id = sqlc.arg(expected_agent_session_id_launch_id)
  AND provider_conversation_id = sqlc.arg(expected_provider_conversation_id)
  AND controller_generation = sqlc.arg(expected_controller_generation);

-- name: CanonicalizeRuntimeHandle :execrows
-- Upgrade one ownership-proven legacy TUI route and its recovered supervisor
-- generation atomically. No activity or user-visible recency changes: this is
-- provenance for the same surviving controller, not a new lifecycle event.
UPDATE sessions SET
    runtime_handle_id = sqlc.arg(canonical_runtime_handle_id),
    runtime_launch_id = sqlc.arg(actual_runtime_launch_id),
    agent_session_id_launch_id = CASE
        WHEN agent_session_id != '' AND
             (agent_session_id_launch_id = '' OR agent_session_id_launch_id = sqlc.arg(expected_runtime_launch_id))
        THEN sqlc.arg(actual_runtime_launch_id)
        ELSE agent_session_id_launch_id
    END
WHERE id = sqlc.arg(id)
  AND session_mode = 'tui'
  AND session_mode = sqlc.arg(expected_session_mode)
  AND is_terminated = 0
  AND is_terminated = sqlc.arg(expected_is_terminated)
  AND harness = sqlc.arg(expected_harness)
  AND runtime_handle_id = sqlc.arg(expected_runtime_handle_id)
  AND runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
  AND agent_session_id = sqlc.arg(expected_agent_session_id)
  AND agent_session_id_launch_id = sqlc.arg(expected_agent_session_id_launch_id)
  AND provider_conversation_id = sqlc.arg(expected_provider_conversation_id)
  AND controller_generation = sqlc.arg(expected_controller_generation);

-- name: ReconcileRuntimeActivity :execrows
-- Startup records an authoritative current terminal observation only after the
-- exact current TUI supervisor generation was proved alive. Full activity +
-- owner + handle fencing keeps a delayed observation from overwriting a newer
-- signal or a replaced/terminated controller.
UPDATE sessions SET
    activity_state = sqlc.arg(recovered_activity_state),
    activity_last_at = sqlc.arg(observed_at),
    updated_at = MAX(updated_at, sqlc.arg(observed_at))
WHERE id = sqlc.arg(id)
  AND session_mode = 'tui'
  AND session_mode = sqlc.arg(expected_session_mode)
  AND is_terminated = 0
  AND is_terminated = sqlc.arg(expected_is_terminated)
  AND activity_state = sqlc.arg(expected_activity_state)
  AND activity_last_at = sqlc.arg(expected_activity_last_at)
  AND harness = sqlc.arg(expected_harness)
  AND runtime_handle_id = sqlc.arg(expected_runtime_handle_id)
  AND runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
  AND agent_session_id = sqlc.arg(expected_agent_session_id)
  AND agent_session_id_launch_id = sqlc.arg(expected_agent_session_id_launch_id)
  AND provider_conversation_id = sqlc.arg(expected_provider_conversation_id)
  AND controller_generation = sqlc.arg(expected_controller_generation);

-- name: LatestNonExitedSessionActivity :one
-- change_log is the immutable history of durable activity facts. Startup uses
-- the latest pre-exit fact only as a compatibility fallback when a surviving
-- legacy TUI cannot expose an authoritative current-screen activity reading.
SELECT
    CAST(json_extract(payload, '$.activity') AS TEXT) AS activity_state,
    created_at AS observed_at
FROM change_log
WHERE session_id = sqlc.arg(session_id)
  AND event_type IN ('session_created', 'session_updated')
  AND json_type(payload, '$.activity') = 'text'
  AND json_extract(payload, '$.activity') IN ('active', 'idle', 'waiting_input', 'blocked')
ORDER BY seq DESC
LIMIT 1;

-- name: RecordSessionLatestUserPrompt :execrows
UPDATE sessions SET
    latest_user_prompt = sqlc.arg(latest_user_prompt),
    latest_user_prompt_at = sqlc.arg(updated_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND is_terminated = 0
  AND updated_at <= sqlc.arg(updated_at);

-- name: RecordSessionHumanMessage :execrows
-- Chat message insertion already owns controller/idempotency fencing. Compare
-- against the dedicated fact timestamp here so unrelated lifecycle writes do
-- not suppress a newer human message.
UPDATE sessions SET
    latest_user_prompt = sqlc.arg(latest_user_prompt),
    latest_user_prompt_at = sqlc.arg(latest_user_prompt_at),
    updated_at = MAX(updated_at, sqlc.arg(latest_user_prompt_at))
WHERE id = sqlc.arg(id)
  AND is_terminated = 0
  AND (latest_user_prompt_at IS NULL OR latest_user_prompt_at <= sqlc.arg(latest_user_prompt_at));

-- name: ClaimChatControllerGeneration :execrows
-- A Chat controller claims ownership before its event goroutine starts, but only
-- if the complete controller owner observed before provider launch is still
-- current. Provider open happens outside SQLite; without this old-owner ->
-- new-generation CAS, a delayed launcher could steal ownership from a replacement
-- that published while the first launcher was doing provider I/O.
UPDATE sessions
SET controller_generation = sqlc.arg(new_controller_generation),
    updated_at = MAX(updated_at, sqlc.arg(claimed_at))
WHERE id = sqlc.arg(id)
  AND session_mode = 'chat'
  AND harness = sqlc.arg(expected_harness)
  AND session_mode = sqlc.arg(expected_session_mode)
  AND is_terminated = sqlc.arg(expected_is_terminated)
  AND runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
  AND agent_session_id = sqlc.arg(expected_agent_session_id)
  AND agent_session_id_launch_id = sqlc.arg(expected_agent_session_id_launch_id)
  AND provider_conversation_id = sqlc.arg(expected_provider_conversation_id)
  AND controller_generation = sqlc.arg(expected_controller_generation);

-- name: CommitChatLiveReconnect :execrows
-- Publish restart recovery only while the exact Chat controller generation that
-- reconnected is still current. The ordered provider snapshot is authoritative
-- for Active, Idle, and WaitingInput, so it replaces stale durable activity from
-- before the detach. This deliberately updates only lifecycle facts: a delayed
-- reconnect must not replay a stale full SessionRecord over a newer controller,
-- rename, pin, or other write.
UPDATE sessions SET
    activity_state = sqlc.arg(recovered_activity_state),
    activity_last_at = sqlc.arg(recovered_activity_at),
    updated_at = MAX(updated_at, sqlc.arg(reconnected_at))
WHERE id = sqlc.arg(id)
  AND session_mode = 'chat'
  AND session_mode = sqlc.arg(expected_session_mode)
  AND is_terminated = 0
  AND is_terminated = sqlc.arg(expected_is_terminated)
  AND harness = sqlc.arg(expected_harness)
  AND runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
  AND agent_session_id = sqlc.arg(expected_agent_session_id)
  AND agent_session_id_launch_id = sqlc.arg(expected_agent_session_id_launch_id)
  AND provider_conversation_id = sqlc.arg(expected_provider_conversation_id)
  AND controller_generation = sqlc.arg(expected_controller_generation);

-- name: ActivateConversationBranchSession :execrows
UPDATE sessions
SET provider_conversation_id = ?, controller_generation = ?, updated_at = ?
WHERE id = ? AND session_mode = 'chat' AND is_terminated = 0;

-- name: CommitSessionControllerEpoch :execrows
-- Lifecycle Manager owns this controller-epoch fact. The source-mode CAS keeps
-- a stale transition from replacing a newer controller, while clearing every
-- process-specific handle prevents either interface from inheriting the
-- other's writer identity.
UPDATE sessions
SET session_mode = ?,
    runtime_handle_id = '',
    runtime_launch_id = '',
    agent_session_id = ?,
    agent_session_id_launch_id = '',
    provider_conversation_id = ?,
    controller_generation = '',
    activity_state = 'idle',
    activity_last_at = ?,
    updated_at = ?
WHERE id = ? AND session_mode = ? AND is_terminated = 0;

-- name: GetSession :one
SELECT id, project_id, num, issue_id, kind, harness,
    activity_state, activity_last_at, is_terminated, branch, workspace_path,
    runtime_handle_id, agent_session_id, agent_session_id_launch_id, prompt,
    created_at, updated_at, display_name, first_signal_at, preview_url,
    preview_revision, cleanup_generation, runtime_launch_id,
    workspace_repo_path, terminate_on_pr_merge, diff_base_sha, diff_base_ref,
    reviewer_harness, reviewer_agent_config, is_pinned, pinned_at,
    session_mode, provider_conversation_id, controller_generation, browser_capability_verifier,
    latest_user_prompt, latest_user_prompt_at, latest_assistant_update, native_transcript_path, auto_inject_review, auto_inject_ci, auto_review_enabled, model
FROM sessions WHERE id = ?;

-- name: ListSessionsByProject :many
SELECT id, project_id, num, issue_id, kind, harness,
    activity_state, activity_last_at, is_terminated, branch, workspace_path,
    runtime_handle_id, agent_session_id, agent_session_id_launch_id, prompt,
    created_at, updated_at, display_name, first_signal_at, preview_url,
    preview_revision, cleanup_generation, runtime_launch_id,
    workspace_repo_path, terminate_on_pr_merge, diff_base_sha, diff_base_ref,
    reviewer_harness, reviewer_agent_config, is_pinned, pinned_at,
    session_mode, provider_conversation_id, controller_generation, browser_capability_verifier,
    latest_user_prompt, latest_user_prompt_at, latest_assistant_update, native_transcript_path, auto_inject_review, auto_inject_ci, auto_review_enabled, model
FROM sessions WHERE project_id = ? ORDER BY num;

-- name: ListAllSessions :many
SELECT id, project_id, num, issue_id, kind, harness,
    activity_state, activity_last_at, is_terminated, branch, workspace_path,
    runtime_handle_id, agent_session_id, agent_session_id_launch_id, prompt,
    created_at, updated_at, display_name, first_signal_at, preview_url,
    preview_revision, cleanup_generation, runtime_launch_id,
    workspace_repo_path, terminate_on_pr_merge, diff_base_sha, diff_base_ref,
    reviewer_harness, reviewer_agent_config, is_pinned, pinned_at,
    session_mode, provider_conversation_id, controller_generation, browser_capability_verifier,
    latest_user_prompt, latest_user_prompt_at, latest_assistant_update, native_transcript_path, auto_inject_review, auto_inject_ci, auto_review_enabled, model
FROM sessions ORDER BY project_id, num;


-- name: RenameSession :execrows
UPDATE sessions SET display_name = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionPreviewURL :execrows
-- preview_revision is bumped on every call (even when preview_url is unchanged)
-- so a repeated `ao preview <same-url>` still trips the sessions_cdc_update
-- trigger and the desktop browser panel re-navigates / refreshes.
UPDATE sessions SET preview_url = ?, preview_revision = preview_revision + 1, updated_at = ? WHERE id = ?;

-- name: SetSessionTerminateOnPRMerge :execrows
UPDATE sessions SET terminate_on_pr_merge = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionAutoInjectReview :execrows
UPDATE sessions SET auto_inject_review = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionAutoInjectCI :execrows
UPDATE sessions SET auto_inject_ci = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionPinned :execrows
UPDATE sessions SET is_pinned = ?, pinned_at = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionReviewerConfig :execrows
UPDATE sessions SET reviewer_harness = ?, reviewer_agent_config = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionAutoReview :execrows
UPDATE sessions SET auto_review_enabled = ?, updated_at = ? WHERE id = ?;

-- name: SessionIsSeed :one
-- SessionIsSeed reports whether the session id matches a row still in seed
-- state (see DeleteSeedSession for the conditions). Callers probe with this
-- before touching change_log so that DeleteSession is a true no-op for live
-- sessions instead of silently destroying their CDC events. Returns 0 when
-- the row does not exist OR has progressed past seed state.
SELECT EXISTS(
    SELECT 1 FROM sessions
    WHERE id = ?
      AND is_terminated = 0
      AND workspace_path = ''
      AND runtime_handle_id = ''
      AND agent_session_id = ''
      AND prompt = ''
      AND latest_user_prompt = ''
      AND latest_assistant_update = ''
      AND native_transcript_path = ''
) AS is_seed;

-- NOTE: the `DELETE FROM sessions WHERE id = ? AND <seed-state predicates>`
-- statement is intentionally NOT a sqlc query — same sqlc 1.31 SQLite-parser
-- bug as documented in queries/changelog.sql: trailing string literals (and
-- placeholders) on the RHS of `=` in a DELETE get silently stripped, so the
-- generated SQL ends up mid-clause and the row count is meaningless. The
-- store runs that DELETE directly via tx.ExecContext inside
-- Store.DeleteSession, inside the same transaction as the SessionIsSeed
-- probe and the raw change_log cleanup.
