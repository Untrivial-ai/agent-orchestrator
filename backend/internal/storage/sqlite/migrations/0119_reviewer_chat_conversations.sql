-- Native Chat conversations owned by durable reviewer rows (version 112).

-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

ALTER TABLE review ADD COLUMN interface_mode TEXT NOT NULL DEFAULT 'tui';
ALTER TABLE review ADD COLUMN provider_conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE review ADD COLUMN controller_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE review ADD COLUMN controller_error TEXT NOT NULL DEFAULT '';

DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS conversation_branch_root_provider_update;
DROP TRIGGER IF EXISTS review_conversation_branch_root_provider_update;
DROP TRIGGER IF EXISTS conversation_turns_branch_insert;
DROP TRIGGER IF EXISTS conversation_messages_branch_insert;
DROP TRIGGER IF EXISTS conversation_activities_branch_insert;
DROP TRIGGER IF EXISTS conversation_provider_events_branch_insert;

CREATE TABLE conversations_next (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL CHECK (scope IN ('session', 'project', 'review')),
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id      TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    review_id       TEXT REFERENCES review(id) ON DELETE CASCADE,
    current_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    current_review_id  TEXT REFERENCES review(id) ON DELETE SET NULL,
    latest_sequence INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    model TEXT,
    reasoning_effort TEXT,
    approval_mode TEXT,
    compacted_at TIMESTAMP,
    context_used INTEGER,
    context_window INTEGER,
    usage_input_tokens INTEGER,
    usage_output_tokens INTEGER,
    usage_cached_tokens INTEGER,
    usage_total_tokens INTEGER,
    rate_limit_primary_percent REAL,
    rate_limit_secondary_percent REAL,
    rate_limit_primary_resets_in INTEGER,
    rate_limit_secondary_resets_in INTEGER,
    rate_limit_plan TEXT,
    provider_title TEXT NOT NULL DEFAULT '',
    applied_title TEXT NOT NULL DEFAULT '',
    model_reroute_json TEXT,
    account_json TEXT,
    thread_state_json TEXT,
    mcp_servers_json TEXT,
    usage_cost REAL,
    usage_currency TEXT,
    active_branch_id TEXT NOT NULL DEFAULT '',
    CHECK (
        (scope = 'session' AND session_id IS NOT NULL AND review_id IS NULL) OR
        (scope = 'project' AND session_id IS NULL AND review_id IS NULL) OR
        (scope = 'review' AND session_id IS NULL AND review_id IS NOT NULL)
    ),
    CHECK (current_session_id IS NULL OR current_review_id IS NULL)
);

INSERT INTO conversations_next (
    id, scope, project_id, session_id, review_id, current_session_id,
    current_review_id, latest_sequence, created_at, updated_at, model,
    reasoning_effort, approval_mode, compacted_at, context_used, context_window,
    usage_input_tokens, usage_output_tokens, usage_cached_tokens,
    usage_total_tokens, rate_limit_primary_percent,
    rate_limit_secondary_percent, rate_limit_primary_resets_in,
    rate_limit_secondary_resets_in, rate_limit_plan, provider_title,
    applied_title, model_reroute_json, account_json, thread_state_json,
    mcp_servers_json, usage_cost, usage_currency, active_branch_id
)
SELECT
    id, scope, project_id, session_id, NULL, current_session_id, NULL,
    latest_sequence, created_at, updated_at, model, reasoning_effort,
    approval_mode, compacted_at, context_used, context_window,
    usage_input_tokens, usage_output_tokens, usage_cached_tokens,
    usage_total_tokens, rate_limit_primary_percent,
    rate_limit_secondary_percent, rate_limit_primary_resets_in,
    rate_limit_secondary_resets_in, rate_limit_plan, provider_title,
    applied_title, model_reroute_json, account_json, thread_state_json,
    mcp_servers_json, usage_cost, usage_currency, active_branch_id
FROM conversations;

DROP TABLE conversations;
ALTER TABLE conversations_next RENAME TO conversations;

CREATE UNIQUE INDEX idx_conversations_session ON conversations(session_id)
    WHERE session_id IS NOT NULL;
CREATE UNIQUE INDEX idx_conversations_project_scope ON conversations(project_id)
    WHERE scope = 'project';
CREATE UNIQUE INDEX idx_conversations_review ON conversations(review_id)
    WHERE review_id IS NOT NULL;
CREATE INDEX idx_conversations_current_session ON conversations(current_session_id)
    WHERE current_session_id IS NOT NULL;
CREATE INDEX idx_conversations_current_review ON conversations(current_review_id)
    WHERE current_review_id IS NOT NULL;

CREATE TABLE conversation_branches_next (
    id                       TEXT PRIMARY KEY,
    conversation_id          TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id               TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    review_id                TEXT REFERENCES review(id) ON DELETE SET NULL,
	provider_conversation_id TEXT NOT NULL DEFAULT '',
	strategy                 TEXT NOT NULL DEFAULT 'native',
	replay_cutoff_sequence   INTEGER NOT NULL DEFAULT 0,
	replay_truncated         INTEGER NOT NULL DEFAULT 0,
	provider_scope_id        TEXT NOT NULL DEFAULT '',
	parent_branch_id         TEXT REFERENCES conversation_branches_next(id) ON DELETE RESTRICT,
    fork_after_turn_id       TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replaced_turn_id         TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT,
    replacement_turn_id      TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL,
    fork_after_sequence      INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMP NOT NULL
);

INSERT INTO conversation_branches_next (
	id, conversation_id, session_id, review_id, provider_conversation_id,
	strategy, replay_cutoff_sequence, replay_truncated, provider_scope_id,
	parent_branch_id, fork_after_turn_id, replaced_turn_id,
    replacement_turn_id, fork_after_sequence, created_at
)
SELECT id, conversation_id, session_id, NULL, provider_conversation_id,
	   strategy, replay_cutoff_sequence, replay_truncated, provider_scope_id,
	   parent_branch_id, fork_after_turn_id, replaced_turn_id,
       replacement_turn_id, fork_after_sequence, created_at
FROM conversation_branches;

DROP TABLE conversation_branches;
ALTER TABLE conversation_branches_next RENAME TO conversation_branches;
CREATE INDEX idx_conversation_branches_lineage
    ON conversation_branches(conversation_id, parent_branch_id, fork_after_sequence);
CREATE INDEX idx_conversation_branches_provider_identity
    ON conversation_branches(conversation_id, provider_conversation_id);
CREATE INDEX idx_conversation_branches_review
    ON conversation_branches(review_id) WHERE review_id IS NOT NULL;

CREATE TABLE conversation_turns_next (
    id                     TEXT PRIMARY KEY,
    conversation_id        TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    handled_by_session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    handled_by_review_id   TEXT REFERENCES review(id) ON DELETE CASCADE,
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
    id, conversation_id, handled_by_session_id, handled_by_review_id,
    provider_turn_id, controller_generation, state, error_message,
    requested_at, started_at, completed_at, diff_json, rolled_back_at,
    plan_json, branch_id, promotion_started_at, promoted_to_turn_id,
    retry_of_turn_id
)
SELECT id, conversation_id, handled_by_session_id, NULL, provider_turn_id,
       controller_generation, state, error_message, requested_at, started_at,
       completed_at, diff_json, rolled_back_at, plan_json, branch_id,
       promotion_started_at, promoted_to_turn_id, retry_of_turn_id
FROM conversation_turns ORDER BY rowid;

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

CREATE TABLE conversation_provider_events_next (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    review_id          TEXT REFERENCES review(id) ON DELETE CASCADE,
    provider_event_id  TEXT NOT NULL DEFAULT '',
    method             TEXT NOT NULL,
    payload_json       TEXT NOT NULL,
    received_at        TIMESTAMP NOT NULL,
    branch_id          TEXT NOT NULL DEFAULT ''
);

INSERT INTO conversation_provider_events_next (
    id, conversation_id, session_id, review_id, provider_event_id, method,
    payload_json, received_at, branch_id
)
SELECT id, conversation_id, session_id, NULL, provider_event_id, method,
       payload_json, received_at, branch_id
FROM conversation_provider_events;

DROP TABLE conversation_provider_events;
ALTER TABLE conversation_provider_events_next RENAME TO conversation_provider_events;
CREATE UNIQUE INDEX idx_conversation_provider_events_dedupe
    ON conversation_provider_events(conversation_id, provider_event_id)
    WHERE provider_event_id <> '';
CREATE INDEX idx_conversation_provider_events_replay
    ON conversation_provider_events(conversation_id, id);

CREATE TRIGGER conversation_turns_branch_insert
AFTER INSERT ON conversation_turns
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_turns
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_messages_branch_insert
AFTER INSERT ON conversation_messages
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_messages
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_activities_branch_insert
AFTER INSERT ON conversation_activities
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_activities
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_provider_events_branch_insert
AFTER INSERT ON conversation_provider_events
WHEN NEW.branch_id = ''
BEGIN
    UPDATE conversation_provider_events
    SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER conversation_branch_root_provider_update
AFTER UPDATE OF provider_conversation_id ON sessions
WHEN OLD.provider_conversation_id = '' AND NEW.provider_conversation_id <> ''
BEGIN
    UPDATE conversation_branches
    SET provider_conversation_id = NEW.provider_conversation_id
    WHERE parent_branch_id IS NULL AND provider_conversation_id = ''
      AND id IN (SELECT active_branch_id FROM conversations WHERE current_session_id = NEW.id);
END;

CREATE TRIGGER review_conversation_branch_root_provider_update
AFTER UPDATE OF provider_conversation_id ON review
WHEN OLD.provider_conversation_id = '' AND NEW.provider_conversation_id <> ''
BEGIN
    UPDATE conversation_branches
    SET provider_conversation_id = NEW.provider_conversation_id
    WHERE parent_branch_id IS NULL AND provider_conversation_id = ''
      AND id IN (SELECT active_branch_id FROM conversations WHERE current_review_id = NEW.id);
END;

CREATE TRIGGER review_conversation_title_cdc_update
AFTER UPDATE OF provider_title ON conversations
WHEN OLD.provider_title <> NEW.provider_title AND NEW.current_review_id IS NOT NULL BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id,
                       'conversationId', NEW.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM review r
    JOIN sessions s ON s.id = r.session_id
    WHERE r.id = NEW.current_review_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert AFTER INSERT ON conversation_messages BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id,
                       'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_update AFTER UPDATE ON conversation_messages
WHEN OLD.revision <> NEW.revision BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id,
                       'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_insert AFTER INSERT ON conversation_activities BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id,
                       'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_update AFTER UPDATE ON conversation_activities
WHEN OLD.revision <> NEW.revision BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id,
                       'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
    FROM conversations c JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_turns_cdc_update AFTER UPDATE ON conversation_turns
WHEN OLD.state <> NEW.state BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'reviewId', NEW.handled_by_review_id,
                       'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.completed_at, NEW.started_at, NEW.requested_at)
    FROM sessions s WHERE s.id = NEW.handled_by_session_id;
END;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Reviewer conversations cannot be represented by the prior schema without
-- deleting durable history. Keep the expanded schema on downgrade.
SELECT 1;
-- +goose StatementEnd
