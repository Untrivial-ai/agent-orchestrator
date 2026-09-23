-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- ACP history replay can prove that a historical turn is no longer live without
-- carrying a portable completed/interrupted/failed outcome. Rebuild the table to
-- admit the shared terminal "recovered" state without rewriting existing facts.
PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;

CREATE TABLE conversation_turns_next (
    id                     TEXT PRIMARY KEY,
    conversation_id        TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    handled_by_session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    provider_turn_id       TEXT NOT NULL DEFAULT '',
    controller_generation  TEXT NOT NULL DEFAULT '',
    state                  TEXT NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'recovered', 'interrupted', 'failed')),
    error_message          TEXT NOT NULL DEFAULT '',
    requested_at           TIMESTAMP NOT NULL,
    started_at             TIMESTAMP,
    completed_at           TIMESTAMP,
    diff_json              TEXT NOT NULL DEFAULT '',
    rolled_back_at         TIMESTAMP,
    plan_json              TEXT NOT NULL DEFAULT '',
    branch_id              TEXT NOT NULL DEFAULT '',
    promotion_started_at   TIMESTAMP,
    promoted_to_turn_id    TEXT REFERENCES conversation_turns_next(id) ON DELETE SET NULL
);

INSERT INTO conversation_turns_next (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, error_message, requested_at, started_at,
    completed_at, diff_json, rolled_back_at, plan_json, branch_id,
    promotion_started_at, promoted_to_turn_id
)
SELECT
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, error_message, requested_at, started_at,
    completed_at, diff_json, rolled_back_at, plan_json, branch_id,
    promotion_started_at, promoted_to_turn_id
FROM conversation_turns
-- Rollback ranges deliberately use rowid to order turns recorded in the same
-- clock tick. Reassign rowids in the original order during the rebuild.
ORDER BY rowid;

DROP TABLE conversation_turns;
ALTER TABLE conversation_turns_next RENAME TO conversation_turns;

CREATE INDEX idx_conversation_turns_conversation
    ON conversation_turns(conversation_id, requested_at);
CREATE UNIQUE INDEX idx_conversation_turns_provider
    ON conversation_turns(conversation_id, provider_turn_id)
    WHERE provider_turn_id <> '';
CREATE INDEX idx_conversation_turns_branch
    ON conversation_turns(branch_id, requested_at);

CREATE TRIGGER conversation_turns_branch_insert
AFTER INSERT ON conversation_turns
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_turns
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_turns_cdc_update
AFTER UPDATE ON conversation_turns
WHEN OLD.state <> NEW.state
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.completed_at, NEW.started_at, NEW.requested_at)
    FROM sessions s
    WHERE s.id = NEW.handled_by_session_id;
END;

-- Pending provider items in a recovered turn have the same missing-outcome
-- evidence as their parent. Keep that distinction instead of inventing failure.
CREATE TABLE conversation_activities_next (
    id                TEXT PRIMARY KEY,
    conversation_id   TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    turn_id           TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL,
    sequence          INTEGER NOT NULL,
    revision          INTEGER NOT NULL DEFAULT 0,
    kind              TEXT NOT NULL CHECK (kind IN ('command', 'file_change', 'plan', 'reasoning', 'approval', 'usage', 'error', 'system', 'mcp_tool', 'auto_review', 'user_input')),
    status            TEXT NOT NULL CHECK (status IN ('running', 'completed', 'recovered', 'failed', 'cancelled', 'pending', 'resolved')),
    summary           TEXT NOT NULL DEFAULT '',
    detail_json       TEXT NOT NULL DEFAULT '',
    request_id        TEXT NOT NULL DEFAULT '',
    provider_item_id  TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL,
    updated_at        TIMESTAMP NOT NULL,
    command_output    TEXT NOT NULL DEFAULT '',
    command_output_truncated INTEGER NOT NULL DEFAULT 0,
    streamed_text     TEXT NOT NULL DEFAULT '',
    streamed_text_truncated  INTEGER NOT NULL DEFAULT 0,
    branch_id         TEXT NOT NULL DEFAULT '',
    UNIQUE (conversation_id, sequence)
);

INSERT INTO conversation_activities_next (
    id, conversation_id, turn_id, sequence, revision, kind, status, summary,
    detail_json, request_id, provider_item_id, created_at, updated_at,
    command_output, command_output_truncated, streamed_text,
    streamed_text_truncated, branch_id
)
SELECT
    id, conversation_id, turn_id, sequence, revision, kind, status, summary,
    detail_json, request_id, provider_item_id, created_at, updated_at,
    command_output, command_output_truncated, streamed_text,
    streamed_text_truncated, branch_id
FROM conversation_activities
ORDER BY sequence;

DROP TABLE conversation_activities;
ALTER TABLE conversation_activities_next RENAME TO conversation_activities;

CREATE UNIQUE INDEX idx_conversation_activities_provider_item
    ON conversation_activities(conversation_id, provider_item_id)
    WHERE provider_item_id <> '';
CREATE UNIQUE INDEX idx_conversation_activities_request
    ON conversation_activities(conversation_id, request_id)
    WHERE request_id <> '';
CREATE INDEX idx_conversation_activities_order
    ON conversation_activities(conversation_id, sequence);
CREATE INDEX idx_conversation_activities_pending
    ON conversation_activities(conversation_id, kind, status);
CREATE INDEX idx_conversation_activities_branch
    ON conversation_activities(branch_id, sequence);

CREATE TRIGGER conversation_activities_branch_insert
AFTER INSERT ON conversation_activities
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_activities
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_activities_cdc_insert
AFTER INSERT ON conversation_activities
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_update
AFTER UPDATE ON conversation_activities
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Recovered outcomes cannot be represented faithfully by the previous schema.
-- Keep the expanded check on downgrade instead of relabelling durable history.
SELECT 1;
-- +goose StatementEnd
