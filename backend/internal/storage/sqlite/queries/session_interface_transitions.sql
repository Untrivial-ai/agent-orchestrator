-- name: InsertSessionInterfaceTransition :one
INSERT INTO session_interface_transitions (
    id, session_id, source_mode, target_mode, policy, phase,
    native_conversation_id, error_code, error_detail,
    created_at, updated_at, completed_at, notice_acknowledged_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, NULL, NULL)
RETURNING id, session_id, source_mode, target_mode, policy, phase,
          native_conversation_id, error_code, error_detail,
          created_at, updated_at, completed_at, notice_acknowledged_at;

-- name: GetSessionInterfaceTransition :one
SELECT id, session_id, source_mode, target_mode, policy, phase,
       native_conversation_id, error_code, error_detail,
       created_at, updated_at, completed_at, notice_acknowledged_at
FROM session_interface_transitions
WHERE id = ?;

-- name: GetActiveSessionInterfaceTransition :one
SELECT id, session_id, source_mode, target_mode, policy, phase,
       native_conversation_id, error_code, error_detail,
       created_at, updated_at, completed_at, notice_acknowledged_at
FROM session_interface_transitions
WHERE session_id = ?
  AND phase NOT IN ('completed', 'failed', 'cancelled', 'recovery_required')
ORDER BY created_at DESC
LIMIT 1;

-- name: GetLatestSessionInterfaceTransition :one
SELECT id, session_id, source_mode, target_mode, policy, phase,
       native_conversation_id, error_code, error_detail,
       created_at, updated_at, completed_at, notice_acknowledged_at
FROM session_interface_transitions
WHERE session_id = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: ListActiveSessionInterfaceTransitions :many
SELECT id, session_id, source_mode, target_mode, policy, phase,
       native_conversation_id, error_code, error_detail,
       created_at, updated_at, completed_at, notice_acknowledged_at
FROM session_interface_transitions
WHERE phase NOT IN ('completed', 'failed', 'cancelled', 'recovery_required')
ORDER BY created_at;

-- name: ListDeliverableSessionInterfaceTransitions :many
SELECT t.id, t.session_id, t.source_mode, t.target_mode, t.policy, t.phase,
       t.native_conversation_id, t.error_code, t.error_detail,
       t.created_at, t.updated_at, t.completed_at, t.notice_acknowledged_at
FROM session_interface_transitions AS t
WHERE t.phase IN ('completed', 'failed', 'cancelled', 'recovery_required')
  AND EXISTS (
      SELECT 1
      FROM session_interface_transition_messages AS m
      WHERE m.transition_id = t.id AND m.delivered_at IS NULL
  )
ORDER BY t.updated_at, t.id;

-- name: AdvanceSessionInterfaceTransition :execrows
UPDATE session_interface_transitions
SET phase = ?, native_conversation_id = ?, error_code = ?, error_detail = ?,
    updated_at = ?, completed_at = ?
WHERE id = ? AND phase = ?;

-- name: AcknowledgeSessionInterfaceTransitionNotice :one
UPDATE session_interface_transitions
SET notice_acknowledged_at = COALESCE(
    notice_acknowledged_at,
    sqlc.arg(notice_acknowledged_at)
)
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND phase IN ('failed', 'recovery_required')
RETURNING id, session_id, source_mode, target_mode, policy, phase,
          native_conversation_id, error_code, error_detail,
          created_at, updated_at, completed_at, notice_acknowledged_at;

-- name: EnqueueSessionInterfaceTransitionMessage :exec
INSERT INTO session_interface_transition_messages (
    transition_id, client_message_id, message, created_at
)
VALUES (?, ?, ?, ?);

-- name: ListPendingSessionInterfaceTransitionMessages :many
SELECT id, transition_id, client_message_id, message, created_at, delivered_at
FROM session_interface_transition_messages
WHERE transition_id = ? AND delivered_at IS NULL
ORDER BY id;

-- name: MarkSessionInterfaceTransitionMessageDelivered :execrows
UPDATE session_interface_transition_messages
SET delivered_at = ?
WHERE id = ? AND delivered_at IS NULL;
