-- Chat conversation storage.
--
-- Ordering has a single writer: NextConversationSequence bumps and returns
-- latest_sequence, and callers use that value for the row they are about to
-- insert. Both statements run inside one store-level transaction so two
-- concurrent writers cannot mint the same position.

-- name: InsertConversation :exec
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id, latest_sequence,
    active_branch_id, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?);

-- name: SelectConversationBySession :one
SELECT * FROM conversations WHERE current_session_id = ? LIMIT 1;

-- name: SelectProjectConversation :one
SELECT * FROM conversations WHERE project_id = ? AND scope = 'project' LIMIT 1;

-- name: BindProjectConversationSession :exec
UPDATE conversations SET current_session_id = ?, updated_at = ?
WHERE id = ? AND scope = 'project';

-- name: SelectConversationByID :one
SELECT * FROM conversations WHERE id = ? LIMIT 1;

-- name: InsertConversationBranch :exec
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id,
    parent_branch_id, fork_after_turn_id, replaced_turn_id,
    replacement_turn_id, fork_after_sequence, strategy, replay_cutoff_sequence,
    replay_truncated, provider_scope_id, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(conversation_id), sqlc.narg(session_id),
    sqlc.arg(provider_conversation_id), sqlc.narg(parent_branch_id),
    sqlc.narg(fork_after_turn_id), sqlc.narg(replaced_turn_id),
    sqlc.narg(replacement_turn_id), sqlc.arg(fork_after_sequence), sqlc.arg(strategy),
    sqlc.arg(replay_cutoff_sequence), sqlc.arg(replay_truncated), sqlc.arg(provider_scope_id), sqlc.arg(created_at)
);

-- name: SelectConversationBranch :one
WITH RECURSIVE lineage(id, parent_branch_id, replaced_turn_id, provider_scope_id, depth) AS (
    SELECT branch.id, branch.parent_branch_id, branch.replaced_turn_id,
           branch.provider_scope_id, 0
    FROM conversation_branches AS branch
    WHERE branch.conversation_id = sqlc.arg(conversation_id)
      AND branch.id = sqlc.arg(branch_id)
    UNION ALL
    SELECT parent.id, parent.parent_branch_id, parent.replaced_turn_id,
           parent.provider_scope_id, lineage.depth + 1
    FROM lineage
    JOIN conversation_branches AS parent ON parent.id = lineage.parent_branch_id
    WHERE parent.conversation_id = sqlc.arg(conversation_id)
)
SELECT b.*, b.id = c.active_branch_id AS active,
       CAST(COALESCE((
           SELECT lineage.provider_scope_id
           FROM lineage
           WHERE lineage.provider_scope_id <> ''
              OR lineage.parent_branch_id IS NULL
              OR lineage.replaced_turn_id IS NULL
           ORDER BY lineage.depth
           LIMIT 1
       ), '') AS TEXT) AS effective_provider_scope_id,
       CAST(COALESCE((
           SELECT lineage.id
           FROM lineage
           WHERE lineage.parent_branch_id IS NULL
              OR lineage.replaced_turn_id IS NULL
           ORDER BY lineage.depth
           LIMIT 1
       ), '') AS TEXT) AS provider_binding_id
FROM conversation_branches AS b
JOIN conversations AS c ON c.id = b.conversation_id
WHERE b.conversation_id = sqlc.arg(conversation_id)
  AND b.id = sqlc.arg(branch_id)
LIMIT 1;

-- name: SelectConversationBranches :many
WITH RECURSIVE lineages(branch_id, id, parent_branch_id, replaced_turn_id, provider_scope_id, depth) AS (
    SELECT branch.id, branch.id, branch.parent_branch_id, branch.replaced_turn_id,
           branch.provider_scope_id, 0
    FROM conversation_branches AS branch
    WHERE branch.conversation_id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT lineage.branch_id, parent.id, parent.parent_branch_id,
           parent.replaced_turn_id, parent.provider_scope_id, lineage.depth + 1
    FROM lineages AS lineage
    JOIN conversation_branches AS parent ON parent.id = lineage.parent_branch_id
    WHERE parent.conversation_id = sqlc.arg(conversation_id)
)
SELECT b.*, b.id = c.active_branch_id AS active,
       CAST(COALESCE((
           SELECT lineage.provider_scope_id
           FROM lineages AS lineage
           WHERE lineage.branch_id = b.id
             AND (lineage.provider_scope_id <> ''
                  OR lineage.parent_branch_id IS NULL
                  OR lineage.replaced_turn_id IS NULL)
           ORDER BY lineage.depth
           LIMIT 1
       ), '') AS TEXT) AS effective_provider_scope_id,
       CAST(COALESCE((
           SELECT lineage.id
           FROM lineages AS lineage
           WHERE lineage.branch_id = b.id
             AND (lineage.parent_branch_id IS NULL OR lineage.replaced_turn_id IS NULL)
           ORDER BY lineage.depth
           LIMIT 1
       ), '') AS TEXT) AS provider_binding_id
FROM conversation_branches AS b
JOIN conversations AS c ON c.id = b.conversation_id
WHERE b.conversation_id = sqlc.arg(conversation_id)
ORDER BY b.created_at, b.id;

-- The selected human prompt must belong to the active lineage. Its immutable
-- sequence determines one ancestry boundary for turns, messages, activities and
-- provider events. The previous provider turn is the fork target; it is empty for
-- the first prompt, which tells the service to start a fresh provider thread.
-- A provider boundary is the nearest ancestor that is either the root or has no
-- replaced turn. Agent switching creates the latter kind: older AO history stays
-- visible, but its provider turn ids can never be sent to the new driver.
-- name: SelectConversationEditAnchor :one
WITH RECURSIVE active_path(branch_id, max_sequence, depth) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER), 0
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END,
           path.depth + 1
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
), active_binding_floor AS (
    SELECT branch.id, branch.fork_after_sequence
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NULL
       OR branch.replaced_turn_id IS NULL
    ORDER BY path.depth
    LIMIT 1
), active_opaque_scope_floor AS (
    SELECT branch.id, branch.fork_after_sequence
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.provider_scope_id <> ''
       OR branch.parent_branch_id IS NULL
       OR branch.replaced_turn_id IS NULL
    ORDER BY path.depth
    LIMIT 1
), active_branch AS (
    SELECT branch.*
    FROM conversations AS conversation
    JOIN conversation_branches AS branch ON branch.id = conversation.active_branch_id
    WHERE conversation.id = sqlc.arg(conversation_id)
), selected_message AS (
    SELECT message.conversation_id,
           message.turn_id,
           message.sequence,
           message.delivery_content_json,
           active_binding_floor.fork_after_sequence AS replay_floor_sequence,
           active_opaque_scope_floor.fork_after_sequence AS opaque_scope_floor_sequence,
           active_branch.parent_branch_id IS NOT NULL
               AND active_branch.replaced_turn_id = message.turn_id
               AND active_branch.replacement_turn_id IS NULL AS retry_active_branch
    FROM conversation_messages AS message
    JOIN active_path AS path ON path.branch_id = message.branch_id
    CROSS JOIN active_branch
    CROSS JOIN active_binding_floor
    CROSS JOIN active_opaque_scope_floor
    WHERE message.conversation_id = sqlc.arg(conversation_id)
      AND message.turn_id = sqlc.arg(replaced_turn_id)
      AND message.role = 'user'
      AND message.origin = 'human'
      AND message.sequence > active_binding_floor.fork_after_sequence
      AND (
          path.max_sequence IS NULL
          OR message.sequence <= path.max_sequence
          OR (
              active_branch.replaced_turn_id = message.turn_id
              AND active_branch.replacement_turn_id IS NULL
          )
      )
    LIMIT 1
)
SELECT selected_message.conversation_id,
       conversation.active_branch_id AS source_branch_id,
       selected_message.turn_id AS replaced_turn_id,
       CAST(COALESCE((
           SELECT previous_turn.provider_turn_id
           FROM conversation_messages AS previous_message
           JOIN conversation_turns AS previous_turn
             ON previous_turn.id = previous_message.turn_id
           JOIN active_path AS previous_path
             ON previous_path.branch_id = previous_message.branch_id
           WHERE previous_message.conversation_id = selected_message.conversation_id
             AND previous_message.role = 'user'
             AND previous_message.sequence < selected_message.sequence
             AND previous_message.sequence > selected_message.opaque_scope_floor_sequence
             AND previous_turn.provider_turn_id <> ''
             AND previous_turn.rolled_back_at IS NULL
             AND (previous_path.max_sequence IS NULL
                  OR previous_message.sequence <= previous_path.max_sequence)
           ORDER BY previous_message.sequence DESC
           LIMIT 1
       ), '') AS TEXT) AS previous_provider_turn_id,
       selected_message.sequence - 1 AS fork_after_sequence,
       selected_message.replay_floor_sequence,
       EXISTS (
           SELECT 1
           FROM conversation_messages AS prior_message
           JOIN active_path AS prior_path ON prior_path.branch_id = prior_message.branch_id
           LEFT JOIN conversation_turns AS prior_turn ON prior_turn.id = prior_message.turn_id
           WHERE prior_message.conversation_id = selected_message.conversation_id
             AND prior_message.sequence > selected_message.replay_floor_sequence
             AND prior_message.sequence < selected_message.sequence
             AND TRIM(prior_message.text) <> ''
             AND (prior_message.turn_id IS NULL OR prior_turn.rolled_back_at IS NULL)
             AND (prior_path.max_sequence IS NULL OR prior_message.sequence <= prior_path.max_sequence)
           UNION ALL
           SELECT 1
           FROM conversation_activities AS prior_activity
           JOIN active_path AS prior_path ON prior_path.branch_id = prior_activity.branch_id
           LEFT JOIN conversation_turns AS prior_turn ON prior_turn.id = prior_activity.turn_id
           WHERE prior_activity.conversation_id = selected_message.conversation_id
             AND prior_activity.sequence > selected_message.replay_floor_sequence
             AND prior_activity.sequence < selected_message.sequence
             -- This row tells AO's UI that a fresh provider context began; it is
             -- not provider-visible context to reconstruct before the first prompt.
             AND prior_activity.provider_item_id NOT LIKE 'ao-context-reset:%'
             AND prior_activity.status <> 'cancelled'
             AND (prior_activity.turn_id IS NULL OR prior_turn.rolled_back_at IS NULL)
             AND (prior_path.max_sequence IS NULL OR prior_activity.sequence <= prior_path.max_sequence)
           LIMIT 1
       ) AS has_prior_context,
       selected_message.delivery_content_json AS original_delivery_content_json,
       selected_message.retry_active_branch
FROM selected_message
JOIN conversations AS conversation ON conversation.id = selected_message.conversation_id;

-- The renderer receives bounded history pages, so it cannot infer a native fork
-- anchor by scanning only the currently loaded turns. Return the first durable,
-- provider-backed human prompt in the active opaque provider scope; every later
-- prompt has a native anchor when the driver advertises fork.
-- name: SelectConversationNativeForkAvailableAfterSequence :one
WITH RECURSIVE active_path(branch_id, max_sequence, depth) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER), 0
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END,
           path.depth + 1
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
), active_opaque_scope_floor AS (
    SELECT branch.fork_after_sequence
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.provider_scope_id <> ''
       OR branch.parent_branch_id IS NULL
       OR branch.replaced_turn_id IS NULL
    ORDER BY path.depth
    LIMIT 1
)
SELECT CAST(COALESCE(MIN(message.sequence), 0) AS INTEGER)
FROM conversation_messages AS message
JOIN conversation_turns AS turn ON turn.id = message.turn_id
JOIN active_path AS path ON path.branch_id = message.branch_id
CROSS JOIN active_opaque_scope_floor
WHERE message.conversation_id = sqlc.arg(conversation_id)
  AND message.role = 'user'
  AND message.origin = 'human'
  AND message.sequence > active_opaque_scope_floor.fork_after_sequence
  AND turn.provider_turn_id <> ''
  AND turn.rolled_back_at IS NULL
  AND (path.max_sequence IS NULL OR message.sequence <= path.max_sequence);

-- name: UpdateConversationBranchReplacement :execrows
UPDATE conversation_branches
SET replacement_turn_id = sqlc.arg(replacement_turn_id)
WHERE conversation_branches.id = sqlc.arg(branch_id)
  AND conversation_branches.conversation_id = (
      SELECT conversation_turns.conversation_id
      FROM conversation_turns
      WHERE conversation_turns.id = sqlc.arg(replacement_turn_id)
  );

-- An edit child is activated before its first prompt is sent so provider events
-- land on the right lineage. If AO stops between those durable steps, startup
-- uses the first human prompt actually written on that child as its replacement.
-- name: SelectFirstHumanTurnOnBranch :one
SELECT conversation_turns.id
FROM conversation_turns
JOIN conversation_messages
  ON conversation_messages.turn_id = conversation_turns.id
 AND conversation_messages.conversation_id = conversation_turns.conversation_id
WHERE conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.branch_id = sqlc.arg(branch_id)
  AND conversation_messages.branch_id = sqlc.arg(branch_id)
  AND conversation_messages.role = 'user'
  AND conversation_messages.origin = 'human'
  AND conversation_turns.rolled_back_at IS NULL
ORDER BY conversation_messages.sequence, conversation_turns.id
LIMIT 1;

-- name: ActivateConversationBranch :execrows
UPDATE conversations
SET active_branch_id = ?, updated_at = ?
WHERE id = ?;

-- The next turn's provider choices. Written only when the user picks something,
-- so NULL keeps meaning "use whatever the conversation was started with".
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationTurnSettings :exec
UPDATE conversations
SET model = ?, reasoning_effort = ?, approval_mode = ?, updated_at = ?
WHERE id = ?;

-- An agent switch starts a new provider/model scope. Clear only the source
-- harness choices; approval posture is AO-owned and remains applicable.
-- name: ResetConversationAgentOverridesForSession :exec
UPDATE conversations
SET model = NULL, reasoning_effort = NULL, updated_at = ?
WHERE current_session_id = ?;

-- Token position for the conversation, latest wins.
--
-- The provider reports this after every tool call, so this UPDATE runs often and
-- deliberately overwrites: there is one current answer to "how full is this
-- conversation", and keeping the history of that answer is what buried the
-- timeline before. updated_at is deliberately NOT touched -- usage arriving is the
-- provider talking about the conversation, not the conversation changing, and
-- bumping it on every tool call would make every chat session look freshly active.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationUsage :exec
UPDATE conversations
SET context_used = ?,
    context_window = ?,
    usage_input_tokens = ?,
    usage_output_tokens = ?,
    usage_cached_tokens = ?,
    usage_total_tokens = ?,
    usage_cost = ?,
    usage_currency = ?
WHERE id = ?;

-- Account quota position, latest wins. Percentages in 0..100; the resets columns
-- are remaining seconds, not the absolute instant the provider sends.
-- name: UpdateConversationRateLimits :exec
UPDATE conversations
SET rate_limit_primary_percent = ?,
    rate_limit_secondary_percent = ?,
    rate_limit_primary_resets_in = ?,
    rate_limit_secondary_resets_in = ?,
    rate_limit_plan = ?
WHERE id = ?;

-- When the conversation's history was last summarized to reclaim context. Set
-- alongside the timeline row so the two cannot disagree about whether a
-- compaction happened.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: MarkConversationCompacted :exec
UPDATE conversations
SET compacted_at = ?, updated_at = ?
WHERE id = ?;

-- The thread title the provider reports for this conversation. Kept even when the
-- user has overridden the AO label, because it is the name the conversation has in
-- the provider's own history.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationProviderTitle :exec
UPDATE conversations
SET provider_title = ?, updated_at = ?
WHERE id = ?;

-- The last title AO pushed into sessions.display_name. It is the compare-and-set
-- witness that lets a later provider title replace a label AO wrote while never
-- replacing one a person chose.
-- name: UpdateConversationAppliedTitle :exec
UPDATE conversations
SET applied_title = ?, updated_at = ?
WHERE id = ?;

-- The provider answering with a model other than the one that was asked for.
-- Latest wins: there is one current answer to "which model is actually replying".
-- updated_at is bumped because unlike a usage report this IS a change to the
-- conversation, and a client that caches on updated_at has to see it.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationModelReroute :exec
UPDATE conversations
SET model_reroute_json = ?, updated_at = ?
WHERE id = ?;

-- The provider account this conversation runs under, including the moment it last
-- asked for credentials AO does not hold. Latest wins.
-- name: UpdateConversationAccount :exec
UPDATE conversations
SET account_json = ?, updated_at = ?
WHERE id = ?;

-- The provider's own lifecycle view of the thread. Latest wins.
--
-- updated_at is deliberately NOT touched here. Thread status flips to active and
-- back to idle on every turn, and bumping updated_at on each would make every chat
-- conversation look freshly modified twice per message. The same reasoning
-- UpdateConversationUsage documents.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationThreadState :exec
UPDATE conversations
SET thread_state_json = ?
WHERE id = ?;

-- Tool server startup state. Latest wins, and updated_at is left alone for the
-- same reason as thread state: every server re-reports on every turn.
-- name: UpdateConversationMcpServers :exec
UPDATE conversations
SET mcp_servers_json = ?
WHERE id = ?;

-- name: NextConversationSequence :one
UPDATE conversations
SET latest_sequence = latest_sequence + 1, updated_at = ?
WHERE id = ?
RETURNING latest_sequence;

-- name: InsertConversationTurn :exec
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, retry_of_turn_id, state, requested_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- A turn the PROVIDER started that AO never dispatched: a compaction runs as its
-- own turn, and so does work resumed inside the provider's own history. Without a
-- row every item it emits correlates to no turn, which silently unpicks the
-- timeline. INSERT OR IGNORE because the provider re-announces a turn on resume.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: AdoptProviderConversationTurn :exec
INSERT OR IGNORE INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, requested_at, started_at
) VALUES (?, ?, ?, ?, ?, 'running', ?, ?);

-- Correlating a provider notification back to its turn happens on every streamed
-- event, so it is a keyed lookup rather than a scan.
-- name: SelectConversationTurnByProviderID :one
SELECT * FROM conversation_turns
WHERE conversation_id = ? AND provider_turn_id = ?
LIMIT 1;

-- name: BindConversationTurnProviderID :exec
UPDATE conversation_turns
SET provider_turn_id = ?, started_at = COALESCE(started_at, ?)
WHERE id = ?;

-- name: MarkConversationTurnStarted :exec
UPDATE conversation_turns
SET state = 'running', started_at = COALESCE(started_at, ?)
WHERE id = ?;

-- name: SettleConversationTurn :exec
UPDATE conversation_turns
SET state = ?, error_message = ?, completed_at = COALESCE(completed_at, ?)
WHERE id = ?;

-- A provider can acknowledge an interrupted/failed turn without first emitting
-- item/completed for the command it killed. Settle those rows with the enclosing
-- turn so clients never show a permanent live spinner for work that has stopped.
-- name: SettleRunningConversationActivitiesForTurn :exec
UPDATE conversation_activities
SET status = sqlc.arg(status), revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE conversation_activities.conversation_id = sqlc.arg(conversation_id)
  AND turn_id = sqlc.arg(turn_id)
  AND status = 'running';

-- The same invariant applies when startup discovers work abandoned by a dead
-- controller. This runs before SettleOrphanedConversationTurns while their turn
-- states still identify the affected rows.
-- name: FailOrphanedConversationActivities :exec
UPDATE conversation_activities
SET status = 'failed', revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE status = 'running'
  AND turn_id IN (
      SELECT id FROM conversation_turns
      WHERE handled_by_session_id = sqlc.arg(handled_by_session_id)
        AND state IN ('queued', 'running')
  );

-- Restart reconciliation: a turn left running by a dead controller is not
-- evidence the work finished, so it is settled honestly rather than silently
-- completed.
-- name: SettleOrphanedConversationTurns :exec
UPDATE conversation_turns
SET state = 'failed',
    error_message = 'controller ended before the turn completed',
    completed_at = ?
WHERE handled_by_session_id = ? AND state IN ('queued', 'running');

-- The running turns visible on the active branch, in the same order as the
-- snapshot. Interrupt uses this exact projection when in-memory turn tracking
-- has lost what the UI is showing; nested provider turns mean more than one row
-- can legitimately be running at once, and Stop must not leave one behind.
-- name: ListVisibleRunningTurnsForConversation :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_turns.provider_turn_id FROM conversation_turns
JOIN active_path AS path ON path.branch_id = conversation_turns.branch_id
WHERE conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.state = 'running'
  AND conversation_turns.promoted_to_turn_id IS NULL
  AND (path.max_sequence IS NULL OR EXISTS (
      SELECT 1 FROM conversation_messages AS lineage_message
      WHERE lineage_message.turn_id = conversation_turns.id
        AND lineage_message.sequence <= path.max_sequence
      UNION ALL
      SELECT 1 FROM conversation_activities AS lineage_activity
      WHERE lineage_activity.turn_id = conversation_turns.id
        AND lineage_activity.sequence <= path.max_sequence
  ))
ORDER BY conversation_turns.requested_at, conversation_turns.rowid;

-- Overwrite the turn's changed-file summary. The provider re-sends the whole diff
-- on every update, so the latest payload is the complete answer and there is
-- nothing to merge with what was there before.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationTurnDiff :execrows
UPDATE conversation_turns
SET diff_json = ?
WHERE conversation_id = ? AND provider_turn_id = ?;

-- Overwrite the turn's plan. The provider re-sends the whole plan on every change,
-- so the latest payload is the complete answer and there is nothing to merge.
--
-- execrows so the caller can tell "recorded" from "no such turn", which is a real
-- case after a restart: a plan can arrive for a provider turn AO never recorded.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: UpdateConversationTurnPlan :execrows
UPDATE conversation_turns
SET plan_json = ?
WHERE conversation_id = ? AND provider_turn_id = ?;

-- name: SelectConversationTurnByID :one
SELECT * FROM conversation_turns WHERE id = ? LIMIT 1;

-- Turns are read in full, rolled-back ones included. They carry rolled_back_at, so
-- a client can report how much an undo discarded instead of watching the timeline
-- silently shrink.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: SelectConversationTurns :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_turns.* FROM conversation_turns
JOIN active_path AS path ON path.branch_id = conversation_turns.branch_id
WHERE conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.promoted_to_turn_id IS NULL
  AND (path.max_sequence IS NULL OR EXISTS (
      SELECT 1 FROM conversation_messages AS lineage_message
      WHERE lineage_message.turn_id = conversation_turns.id
        AND lineage_message.sequence <= path.max_sequence
      UNION ALL
      SELECT 1 FROM conversation_activities AS lineage_activity
      WHERE lineage_activity.turn_id = conversation_turns.id
        AND lineage_activity.sequence <= path.max_sequence
  ))
ORDER BY conversation_turns.requested_at, conversation_turns.rowid;

-- Turns represented by one bounded timeline page. Active turns are included
-- even before their first item arrives, so the live-turn controls never vanish.
-- name: SelectConversationTurnsPage :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_turns.* FROM conversation_turns
JOIN active_path AS path ON path.branch_id = conversation_turns.branch_id
WHERE conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.promoted_to_turn_id IS NULL
  AND (path.max_sequence IS NULL OR EXISTS (
      SELECT 1 FROM conversation_messages AS lineage_message
      WHERE lineage_message.turn_id = conversation_turns.id
        AND lineage_message.sequence <= path.max_sequence
      UNION ALL
      SELECT 1 FROM conversation_activities AS lineage_activity
      WHERE lineage_activity.turn_id = conversation_turns.id
        AND lineage_activity.sequence <= path.max_sequence
  ))
  AND (
    conversation_turns.state IN ('queued', 'running')
    OR conversation_turns.id IN (
      SELECT turn_id FROM conversation_messages
      WHERE conversation_messages.conversation_id = sqlc.arg(conversation_id)
        AND turn_id IS NOT NULL
        AND conversation_messages.sequence >= sqlc.arg(oldest_sequence)
        AND conversation_messages.sequence < sqlc.arg(before_sequence)
      UNION
      SELECT turn_id FROM conversation_activities
      WHERE conversation_activities.conversation_id = sqlc.arg(conversation_id)
        AND turn_id IS NOT NULL
        AND conversation_activities.sequence >= sqlc.arg(oldest_sequence)
        AND conversation_activities.sequence < sqlc.arg(before_sequence)
    )
  )
ORDER BY conversation_turns.requested_at, conversation_turns.rowid;

-- Everything from the named turn to the end of the conversation, which is the range
-- an undo discards. Inclusive of the named turn: the state a person wants back is
-- the one from before they sent the message they pointed at.
--
-- The cut is on rowid rather than requested_at because rowid is assigned by the
-- insert and strictly increasing, so it orders turns recorded in the same clock tick
-- the same way SelectConversationTurns does. Two turns sharing a timestamp is
-- ordinary; two turns sharing a rowid is impossible.
-- name: MarkConversationTurnsRolledBack :execrows
UPDATE conversation_turns
SET rolled_back_at = ?
WHERE conversation_turns.conversation_id = ?
  AND conversation_turns.rolled_back_at IS NULL
  AND conversation_turns.rowid >= (
      SELECT anchor.rowid FROM conversation_turns AS anchor WHERE anchor.id = ?
  );

-- A discarded turn that never reached the provider settles as interrupted rather
-- than staying queued forever. Nothing went wrong and nothing failed: the user
-- undid the conversation out from under a message that was still waiting, and
-- dispatching it later would send it against a history it was never written for.
-- name: InterruptRolledBackQueuedTurns :exec
UPDATE conversation_turns
SET state = 'interrupted', completed_at = ?
WHERE conversation_id = ?
  AND state IN ('queued', 'running')
  AND rolled_back_at IS NOT NULL;

-- Older AO builds stored compaction boundaries without their provider turn.
-- Correlate those at or after the rollback anchor so the normal rolled-back-turn
-- filter hides facts the provider has now forgotten.
-- name: AttachLegacyCompactionsToRollbackAnchor :exec
UPDATE conversation_activities
SET turn_id = sqlc.arg(anchor_turn_id),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE conversation_activities.conversation_id = sqlc.arg(target_conversation_id)
  AND turn_id IS NULL
  AND kind = 'system'
  AND json_extract(detail_json, '$.event') = 'compaction'
  AND sequence >= COALESCE((
      SELECT MIN(sequence) FROM (
          SELECT sequence FROM conversation_messages WHERE turn_id = sqlc.arg(anchor_turn_id)
          UNION ALL
          SELECT sequence FROM conversation_activities WHERE turn_id = sqlc.arg(anchor_turn_id)
      )
  ), 0);

-- Conversation state must describe the latest compaction that still exists in
-- provider history after rollback, not the latest one AO ever observed.
-- name: RecomputeConversationCompactedAt :exec
UPDATE conversations
SET compacted_at = (
      SELECT MAX(activity.created_at)
      FROM conversation_activities AS activity
      WHERE activity.conversation_id = conversations.id
        AND activity.kind = 'system'
        AND json_extract(activity.detail_json, '$.event') = 'compaction'
        AND (activity.turn_id IS NULL OR activity.turn_id NOT IN (
            SELECT discarded.id FROM conversation_turns AS discarded
            WHERE discarded.conversation_id = conversations.id
              AND discarded.rolled_back_at IS NOT NULL
        ))
    ),
    updated_at = sqlc.arg(updated_at)
WHERE conversations.id = sqlc.arg(target_conversation_id);

-- An approval inside a discarded turn can never be answered: the provider call it
-- was blocking belongs to history the provider has dropped. A rollback is refused
-- while a turn runs, so in practice there is nothing here to close; the statement
-- exists so the invariant is enforced rather than argued.
-- name: FailRolledBackConversationApprovals :exec
UPDATE conversation_activities
SET status = 'failed', revision = revision + 1, updated_at = ?
WHERE conversation_activities.conversation_id = ?
  AND conversation_activities.kind = 'approval'
  AND conversation_activities.status = 'pending'
  AND conversation_activities.turn_id IN (
      SELECT discarded.id FROM conversation_turns AS discarded
      WHERE discarded.conversation_id = ? AND discarded.rolled_back_at IS NOT NULL
  );

-- The head of the send queue: the oldest message recorded while the agent was
-- busy. Joined to its own user message because dispatching needs the text, and
-- the queue is durable rows rather than controller memory, so a restart can see
-- what was never sent.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: SelectNextQueuedConversationTurn :one
SELECT conversation_turns.id,
       conversation_messages.text,
       conversation_messages.client_message_id,
       conversation_messages.origin,
       conversation_messages.delivery_content_json
FROM conversation_turns
JOIN conversation_messages
    ON conversation_messages.turn_id = conversation_turns.id
    AND conversation_messages.role = 'user'
WHERE conversation_turns.conversation_id = ?
  AND conversation_turns.state = 'queued'
  AND conversation_turns.promotion_started_at IS NULL
ORDER BY conversation_turns.requested_at, conversation_turns.rowid
LIMIT 1;

-- Claim one selected queue item before contacting the provider. execrows is the
-- compare-and-set result: zero means the turn is absent, settled, or already being
-- promoted, and the provider must not be called.
-- name: ReserveQueuedConversationTurnForPromotion :execrows
UPDATE conversation_turns
SET promotion_started_at = sqlc.arg(promotion_started_at)
WHERE id = sqlc.arg(id)
  AND conversation_id = sqlc.arg(conversation_id)
  AND state = 'queued'
  AND promotion_started_at IS NULL;

-- The content is loaded after the compare-and-set, from AO's durable message
-- rather than from a client request that could be stale or substituted.
-- name: SelectReservedConversationTurnForPromotion :one
SELECT conversation_turns.id,
       conversation_messages.text,
       conversation_messages.client_message_id,
       conversation_messages.origin,
       conversation_messages.delivery_content_json
FROM conversation_turns
JOIN conversation_messages
    ON conversation_messages.turn_id = conversation_turns.id
    AND conversation_messages.role = 'user'
WHERE conversation_turns.id = sqlc.arg(id)
  AND conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.state = 'queued'
  AND conversation_turns.promotion_started_at IS NOT NULL
LIMIT 1;

-- A provider refusal returns the item to exactly the queue position it already
-- owned; requested_at is deliberately untouched.
-- name: ReleaseQueuedConversationTurnPromotion :execrows
UPDATE conversation_turns
SET promotion_started_at = NULL
WHERE id = sqlc.arg(id)
  AND conversation_id = sqlc.arg(conversation_id)
  AND state = 'queued'
  AND promotion_started_at IS NOT NULL;

-- The provider has accepted the guidance. Link the durable source to the AO turn
-- that absorbed it and take it out of the queue in the same transaction that
-- inserts the visible steer activity.
-- name: CompleteQueuedConversationTurnPromotion :execrows
UPDATE conversation_turns
SET state = 'completed',
    completed_at = sqlc.arg(completed_at),
    promotion_started_at = NULL,
    promoted_to_turn_id = sqlc.arg(promoted_to_turn_id)
WHERE id = sqlc.arg(id)
  AND conversation_id = sqlc.arg(conversation_id)
  AND state = 'queued'
  AND promotion_started_at IS NOT NULL;

-- Stopping the agent stops the queue with it: a brake that starts new work
-- instead of ending it would be the wrong shape for the button the user pressed.
-- The cutoff is the moment the user pressed stop, so a message typed after that
-- is still delivered rather than swept up by a cancellation it predates.
-- name: CancelQueuedConversationTurns :exec
UPDATE conversation_turns
SET state = 'interrupted', completed_at = ?
WHERE conversation_id = ? AND state = 'queued' AND requested_at <= ?;

-- An interrupt interface handoff closes intake under the controller's dispatch
-- lock before this runs. There can be no later accepted row to preserve, so the
-- correct operation is independent of wall-clock ordering and cancels the whole
-- durable queue.
-- name: CancelAllQueuedConversationTurns :exec
UPDATE conversation_turns
SET state = 'interrupted', completed_at = ?
WHERE conversation_id = ? AND state = 'queued';

-- name: InsertConversationMessage :exec
INSERT INTO conversation_messages (
    id, conversation_id, turn_id, sequence, revision, role, origin,
    text, streaming, provider_item_id, client_message_id, delivery_content_json,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- Folding a streaming delta: append to the existing text and bump the revision
-- so a client can detect a gap. The provider item id is the correlation key
-- because AO does not know the message id the provider will use.
-- name: AppendConversationMessageDelta :exec
UPDATE conversation_messages
SET text = text || ?, revision = revision + 1, streaming = 1, updated_at = ?
WHERE conversation_id = ? AND provider_item_id = ?;

-- name: SettleConversationMessage :exec
UPDATE conversation_messages
SET text = ?, revision = revision + 1, streaming = 0, updated_at = ?
WHERE conversation_id = ? AND provider_item_id = ?;

-- name: SelectConversationMessageByProviderItem :one
SELECT * FROM conversation_messages
WHERE conversation_id = ? AND provider_item_id = ?
LIMIT 1;

-- name: SelectConversationMessageByClientID :one
SELECT * FROM conversation_messages
WHERE conversation_id = ? AND client_message_id = ?
LIMIT 1;

-- A native history import has the provider turn identity but not AO's turn id.
-- Looking through the turn also detects a message AO wrote before dispatch, which
-- prevents a Chat -> TUI -> Chat cycle from rendering the same prompt twice.
-- name: SelectConversationUserMessageByTurn :one
SELECT conversation_messages.*
FROM conversation_messages
JOIN conversation_turns ON conversation_turns.id = conversation_messages.turn_id
WHERE conversation_messages.conversation_id = ?
  AND conversation_turns.provider_turn_id = ?
  AND conversation_messages.role = 'user'
LIMIT 1;

-- Prose the agent still remembers. Rows belonging to a rolled-back turn are left
-- out: rollback discarded them provider-side, and showing a person a message the
-- agent has no memory of is the one way this feature can lie.
--
-- Rows with turn_id IS NULL survive the filter on purpose. Those are items the
-- provider never attributed to a turn, and hiding what AO cannot prove belonged to
-- the discarded range would be a guess dressed up as a fact.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: SelectConversationMessages :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_messages.* FROM conversation_messages
JOIN active_path AS path ON path.branch_id = conversation_messages.branch_id
WHERE conversation_messages.conversation_id = sqlc.arg(conversation_id)
  AND (path.max_sequence IS NULL OR conversation_messages.sequence <= path.max_sequence)
  AND (conversation_messages.turn_id IS NULL OR conversation_messages.turn_id NOT IN (
      SELECT discarded.id FROM conversation_turns AS discarded
      WHERE discarded.conversation_id = sqlc.arg(conversation_id)
        AND (discarded.rolled_back_at IS NOT NULL OR discarded.promoted_to_turn_id IS NOT NULL)
  ))
ORDER BY conversation_messages.sequence;

-- name: SelectConversationMessagesPage :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_messages.* FROM conversation_messages
JOIN active_path AS path ON path.branch_id = conversation_messages.branch_id
WHERE conversation_messages.conversation_id = sqlc.arg(conversation_id)
  AND conversation_messages.sequence < sqlc.arg(before_sequence)
  AND (path.max_sequence IS NULL OR conversation_messages.sequence <= path.max_sequence)
  AND (conversation_messages.turn_id IS NULL OR conversation_messages.turn_id NOT IN (
      SELECT discarded.id FROM conversation_turns AS discarded
      WHERE discarded.conversation_id = sqlc.arg(conversation_id)
        AND (discarded.rolled_back_at IS NOT NULL OR discarded.promoted_to_turn_id IS NOT NULL)
  ))
ORDER BY conversation_messages.sequence DESC
LIMIT sqlc.arg(page_limit);

-- name: InsertConversationActivity :exec
INSERT INTO conversation_activities (
    id, conversation_id, turn_id, sequence, revision, kind, status,
    summary, detail_json, request_id, provider_item_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SettleConversationActivity :exec
UPDATE conversation_activities
SET status = ?, summary = ?, detail_json = ?, revision = revision + 1, updated_at = ?
WHERE conversation_id = ? AND provider_item_id = ? AND status <> 'cancelled';

-- Resolving an approval matches on the provider's request id, so a card the user
-- left on screen cannot answer a request that replaced it.
-- Merge the resolution into the provider payload so the offered decision kinds
-- remain available to audit/history readers after the request is answered.
-- name: ResolveConversationApproval :exec
UPDATE conversation_activities
SET status = 'resolved',
    detail_json = json_patch(detail_json, CAST(sqlc.arg(detail_json) AS TEXT)),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE conversation_id = sqlc.arg(conversation_id)
  AND request_id = sqlc.arg(request_id)
  AND status = 'pending';

-- Any approval still pending when a controller dies can never be answered: the
-- provider call it was blocking is gone.
-- name: FailPendingConversationApprovals :exec
UPDATE conversation_activities
SET status = 'failed', revision = revision + 1, updated_at = ?
WHERE conversation_id = ? AND kind = 'approval' AND status = 'pending';

-- A structured input request is held by an in-memory provider RPC just like an
-- approval. If that controller disappears, the old card must stop accepting
-- answers because there is no longer a provider call to receive one.
-- name: FailPendingConversationInputs :exec
UPDATE conversation_activities
SET status = 'failed', revision = revision + 1, updated_at = ?
WHERE conversation_id = ? AND kind = 'user_input' AND status = 'pending';

-- A project conversation can move to a new orchestrator while the old provider
-- stream is still closing. Settle only requests owned by turns from that old
-- session; conversation-wide cleanup would also fail the replacement's requests.
-- name: FailPendingConversationRequestsForSession :exec
UPDATE conversation_activities
SET status = 'failed', revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE conversation_activities.conversation_id = sqlc.arg(target_conversation_id)
  AND kind IN ('approval', 'user_input')
  AND status = 'pending'
  AND turn_id IN (
    SELECT id
    FROM conversation_turns
    WHERE conversation_turns.conversation_id = sqlc.arg(target_conversation_id)
      AND handled_by_session_id = sqlc.arg(handled_by_session_id)
  );

-- Append streamed command output, capped in one statement.
--
-- The cap is applied here rather than in Go so the read, the concatenation, and
-- the truncation flag commit atomically: a runaway command must not be able to
-- interleave a second delta between a length check and the write that acts on it.
-- substr() does the capping and the CASE records that it happened, so output is
-- never silently cut.
--
-- SQLite length() on TEXT counts characters, not bytes, so the byte ceiling is up
-- to 4x max_output_chars in pathological UTF-8. That is still bounded and far
-- below the megabytes-per-row this exists to prevent; command output is
-- overwhelmingly ASCII.
--
-- execrows so the caller can tell "appended" from "no such activity yet", which
-- is a real case: a delta can arrive before the item/started that creates the row.
-- Once truncation is recorded, later deltas are deliberate no-ops. Keeping the
-- revision stable prevents the activity CDC trigger from invalidating every live
-- conversation client for text the row can no longer retain.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: AppendConversationActivityOutput :execrows
UPDATE conversation_activities
SET command_output = substr(command_output || sqlc.arg(delta), 1, sqlc.arg(max_output_chars)),
    command_output_truncated = CASE
        WHEN length(command_output) + length(sqlc.arg(delta)) > sqlc.arg(max_output_chars) THEN 1
        ELSE command_output_truncated
    END,
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE conversation_id = sqlc.arg(conversation_id)
  AND provider_item_id = sqlc.arg(provider_item_id)
  AND status <> 'cancelled'
  AND command_output_truncated = 0;

-- Append provider prose streamed for one activity, capped in one statement.
--
-- Same shape and same reasoning as AppendConversationActivityOutput, against a
-- different column: this is the provider's own text for the item (reasoning
-- summary, terminal keystrokes, tool progress), not a program's output. The two are
-- kept apart because a reader must be able to tell them apart, and because a PTY
-- echoes what is typed, so merging them would show every keystroke twice.
--
-- execrows so the caller can tell "appended" from "no such activity yet", which is
-- a real case: a reasoning delta can arrive before the item/started that creates
-- the row.
-- As with command output, a capped stream stays immutable so a provider that keeps
-- emitting cannot create a no-visible-change CDC storm.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: AppendConversationActivityStreamedText :execrows
UPDATE conversation_activities
SET streamed_text = substr(streamed_text || sqlc.arg(delta), 1, sqlc.arg(max_text_chars)),
    streamed_text_truncated = CASE
        WHEN length(streamed_text) + length(sqlc.arg(delta)) > sqlc.arg(max_text_chars) THEN 1
        ELSE streamed_text_truncated
    END,
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE conversation_id = sqlc.arg(conversation_id)
  AND provider_item_id = sqlc.arg(provider_item_id)
  AND status <> 'cancelled'
  AND streamed_text_truncated = 0;

-- A capped delta is still associated with a real activity. Append callers use
-- this probe to distinguish that harmless no-op from the ordinary provider race
-- where output arrives before item/started creates the activity.
-- name: ConversationActivityExistsForProviderItem :one
SELECT EXISTS(
    SELECT 1
    FROM conversation_activities
    WHERE conversation_id = sqlc.arg(conversation_id)
      AND provider_item_id = sqlc.arg(provider_item_id)
      AND status <> 'cancelled'
);

-- Replace the streamed text with the provider's settled version.
--
-- Reasoning arrives twice: as deltas while the model works, and as the item's own
-- summary array when it completes. The settled form is authoritative, so it
-- replaces rather than appends -- the same relationship SettleConversationMessage
-- has with AppendConversationMessageDelta, and for the same reason: a client that
-- accumulated a dropped delta would otherwise keep a gap forever.
-- NOTE: keep these comments ASCII. sqlc locates its star-expansion edits by byte
-- offset, so a multi-byte character here silently corrupts later queries.
-- name: SettleConversationActivityStreamedText :execrows
UPDATE conversation_activities
SET streamed_text = ?, streamed_text_truncated = 0, revision = revision + 1, updated_at = ?
WHERE conversation_id = ? AND provider_item_id = ? AND status <> 'cancelled';

-- name: SelectConversationActivityByProviderItem :one
SELECT * FROM conversation_activities
WHERE conversation_id = ? AND provider_item_id = ?
LIMIT 1;

-- name: SelectConversationContextResetSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER) AS sequence
FROM conversation_activities
WHERE conversation_id = ?
  AND kind = 'system'
  AND provider_item_id = ?;

-- Activities the agent still remembers, filtered the same way messages are and for
-- the same reason. See SelectConversationMessages.
-- name: SelectConversationActivities :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_activities.* FROM conversation_activities
JOIN active_path AS path ON path.branch_id = conversation_activities.branch_id
WHERE conversation_activities.conversation_id = sqlc.arg(conversation_id)
  AND (path.max_sequence IS NULL OR conversation_activities.sequence <= path.max_sequence)
  AND (conversation_activities.turn_id IS NULL OR conversation_activities.turn_id NOT IN (
      SELECT discarded.id FROM conversation_turns AS discarded
      WHERE discarded.conversation_id = sqlc.arg(conversation_id) AND discarded.rolled_back_at IS NOT NULL
  ))
ORDER BY conversation_activities.sequence;

-- name: SelectConversationActivitiesPage :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_activities.* FROM conversation_activities
JOIN active_path AS path ON path.branch_id = conversation_activities.branch_id
WHERE conversation_activities.conversation_id = sqlc.arg(conversation_id)
  AND conversation_activities.sequence < sqlc.arg(before_sequence)
  AND (path.max_sequence IS NULL OR conversation_activities.sequence <= path.max_sequence)
  AND (conversation_activities.turn_id IS NULL OR conversation_activities.turn_id NOT IN (
      SELECT discarded.id FROM conversation_turns AS discarded
      WHERE discarded.conversation_id = sqlc.arg(conversation_id)
        AND discarded.rolled_back_at IS NOT NULL
  ))
ORDER BY conversation_activities.sequence DESC
LIMIT sqlc.arg(page_limit);

-- Push a provider thread title into the session label, without ever overwriting a
-- name a person chose.
--
-- One statement, not a read followed by a write: a manual rename landing between the
-- two would be silently discarded. The guard admits exactly two cases - the session
-- has no label yet, or it still carries the title AO last wrote - so anything a user
-- typed wins by simply not matching.
--
-- It lives with the conversation queries rather than the session ones because it is
-- part of the conversation title lifecycle; sessions is only the table the value
-- lands in.
-- name: ApplyConversationTitleToSession :execrows
UPDATE sessions
SET display_name = ?, updated_at = ?
WHERE id = ?
  AND (display_name = '' OR display_name = ?);

-- The raw provider event archive. Append-only, and the only way to answer "what
-- did the provider actually say" once a projection turns out to be wrong.
-- name: InsertConversationProviderEvent :execrows
INSERT OR IGNORE INTO conversation_provider_events (
    conversation_id, session_id, provider_event_id, method, payload_json, received_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: SelectConversationProviderEvents :many
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_provider_events.* FROM conversation_provider_events
JOIN active_path AS path ON path.branch_id = conversation_provider_events.branch_id
WHERE conversation_provider_events.conversation_id = sqlc.arg(conversation_id)
  AND conversation_provider_events.id > sqlc.arg(id)
ORDER BY conversation_provider_events.id
LIMIT sqlc.arg(page_limit);
-- A retry re-dispatches a failed turn's durable prompt as a NEW turn. Content is
-- loaded from AO's own rows, never from the caller, so the daemon owns what gets
-- sent again.
-- name: SelectRetryableConversationPrompt :one
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT conversation_messages.text,
       conversation_messages.origin,
       conversation_messages.delivery_content_json,
       EXISTS (
           SELECT 1
           FROM active_path AS path
           WHERE path.branch_id = conversation_messages.branch_id
             AND (path.max_sequence IS NULL OR conversation_messages.sequence <= path.max_sequence)
       ) AS active_lineage
FROM conversation_turns
JOIN conversation_messages
    ON conversation_messages.turn_id = conversation_turns.id
    AND conversation_messages.role = 'user'
WHERE conversation_turns.id = sqlc.arg(id)
  AND conversation_turns.conversation_id = sqlc.arg(conversation_id)
  AND conversation_turns.state = 'failed'
LIMIT 1;

-- A replayed retry request returns the attempt already linked to its source.
-- The relation is daemon-owned rather than inferred from caller-controlled text.
-- name: SelectConversationRetryTurnIDBySource :one
SELECT id FROM conversation_turns
WHERE conversation_id = sqlc.arg(conversation_id)
  AND retry_of_turn_id = sqlc.arg(retry_of_turn_id)
LIMIT 1;

-- Retry attempts outside the active branch still consume their source action.
-- name: SelectConversationRetriedSourceTurnIDs :many
SELECT CAST(retry_of_turn_id AS TEXT) AS retry_of_turn_id
FROM conversation_turns
WHERE conversation_id = sqlc.arg(conversation_id)
  AND retry_of_turn_id IS NOT NULL;
