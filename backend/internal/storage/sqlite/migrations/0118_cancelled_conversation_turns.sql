-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- A queued turn removed from the dock before dispatch is not an interrupt: the
-- user explicitly withdrew it, and it must not reappear as "interrupted by you".
-- Stop and handoff still settle the durable queue as interrupted.
PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_branch_insert;

CREATE TABLE conversation_turns_next (
    id                     TEXT PRIMARY KEY,
    conversation_id        TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    handled_by_session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    provider_turn_id       TEXT NOT NULL DEFAULT '',
    controller_generation  TEXT NOT NULL DEFAULT '',
    state                  TEXT NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'recovered', 'interrupted', 'failed', 'cancelled')),
    error_message          TEXT NOT NULL DEFAULT '',
    requested_at           TIMESTAMP NOT NULL,
    started_at             TIMESTAMP,
    completed_at           TIMESTAMP,
    diff_json              TEXT NOT NULL DEFAULT '',
    rolled_back_at         TIMESTAMP,
    plan_json              TEXT NOT NULL DEFAULT '',
    branch_id              TEXT NOT NULL DEFAULT '',
    promotion_started_at   TIMESTAMP,
    promoted_to_turn_id    TEXT REFERENCES conversation_turns_next(id) ON DELETE SET NULL,
    retry_of_turn_id       TEXT REFERENCES conversation_turns_next(id) ON DELETE RESTRICT
);

INSERT INTO conversation_turns_next (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, error_message, requested_at, started_at,
    completed_at, diff_json, rolled_back_at, plan_json, branch_id,
    promotion_started_at, promoted_to_turn_id, retry_of_turn_id
)
SELECT
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, error_message, requested_at, started_at,
    completed_at, diff_json, rolled_back_at, plan_json, branch_id,
    promotion_started_at, promoted_to_turn_id, retry_of_turn_id
FROM conversation_turns
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
CREATE INDEX idx_conversation_turns_retry_source
    ON conversation_turns(conversation_id, retry_of_turn_id)
    WHERE retry_of_turn_id IS NOT NULL;

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

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Cancelled outcomes cannot be represented faithfully by the previous schema.
SELECT 1;
-- +goose StatementEnd
