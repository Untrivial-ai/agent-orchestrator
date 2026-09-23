-- Durable native Chat conversations owned by reviewer rows or standalone reviews.
--
-- This deliberately follows the current conversation schema rather than the
-- superseded 0129 draft: intervening migrations added provider-history and
-- branch provenance fields which must survive the table rebuild.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

-- SQLite validates trigger bodies while rebuilding a table, so detach every
-- trigger that names conversations before dropping it. They are recreated by
-- the subsequent reviewer-chat controller migration.
DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS conversation_branch_root_provider_update;
DROP TRIGGER IF EXISTS conversation_turns_branch_insert;
DROP TRIGGER IF EXISTS conversation_messages_branch_insert;
DROP TRIGGER IF EXISTS conversation_activities_branch_insert;
DROP TRIGGER IF EXISTS conversation_provider_events_branch_insert;

ALTER TABLE review ADD COLUMN interface_mode TEXT NOT NULL DEFAULT 'tui'
    CHECK (interface_mode IN ('tui', 'chat'));
ALTER TABLE review ADD COLUMN provider_conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE review ADD COLUMN controller_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE review ADD COLUMN controller_error TEXT NOT NULL DEFAULT '';

CREATE TABLE conversations_next (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN ('session', 'project', 'review')),
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    review_id TEXT REFERENCES review(id) ON DELETE CASCADE,
    current_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    current_review_id TEXT REFERENCES review(id) ON DELETE SET NULL,
    latest_sequence INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    model TEXT, reasoning_effort TEXT, approval_mode TEXT, compacted_at TIMESTAMP,
    context_used INTEGER, context_window INTEGER, usage_input_tokens INTEGER,
    usage_output_tokens INTEGER, usage_cached_tokens INTEGER, usage_total_tokens INTEGER,
    rate_limit_primary_percent REAL, rate_limit_secondary_percent REAL,
    rate_limit_primary_resets_in INTEGER, rate_limit_secondary_resets_in INTEGER,
    rate_limit_plan TEXT, provider_title TEXT NOT NULL DEFAULT '', applied_title TEXT NOT NULL DEFAULT '',
    model_reroute_json TEXT, account_json TEXT, thread_state_json TEXT, mcp_servers_json TEXT,
    usage_cost REAL, usage_currency TEXT, active_branch_id TEXT NOT NULL DEFAULT '',
    opencode_mode TEXT NOT NULL DEFAULT '',
    CHECK ((scope = 'session' AND session_id IS NOT NULL AND review_id IS NULL)
        OR (scope = 'project' AND session_id IS NULL AND review_id IS NULL)
        OR (scope = 'review' AND session_id IS NULL AND review_id IS NOT NULL)),
    CHECK (current_session_id IS NULL OR current_review_id IS NULL)
);

INSERT INTO conversations_next SELECT
    id, scope, project_id, session_id, NULL, current_session_id, NULL,
    latest_sequence, created_at, updated_at, model, reasoning_effort, approval_mode, compacted_at,
    context_used, context_window, usage_input_tokens, usage_output_tokens, usage_cached_tokens,
    usage_total_tokens, rate_limit_primary_percent, rate_limit_secondary_percent,
    rate_limit_primary_resets_in, rate_limit_secondary_resets_in, rate_limit_plan,
    provider_title, applied_title, model_reroute_json, account_json, thread_state_json,
    mcp_servers_json, usage_cost, usage_currency, active_branch_id, opencode_mode
FROM conversations;

DROP TABLE conversations;
ALTER TABLE conversations_next RENAME TO conversations;
CREATE UNIQUE INDEX idx_conversations_session ON conversations(session_id) WHERE session_id IS NOT NULL;
CREATE UNIQUE INDEX idx_conversations_project_scope ON conversations(project_id) WHERE scope = 'project';
CREATE UNIQUE INDEX idx_conversations_review ON conversations(review_id) WHERE review_id IS NOT NULL;
CREATE INDEX idx_conversations_current_session ON conversations(current_session_id) WHERE current_session_id IS NOT NULL;
CREATE INDEX idx_conversations_current_review ON conversations(current_review_id) WHERE current_review_id IS NOT NULL;

ALTER TABLE conversation_branches ADD COLUMN review_id TEXT REFERENCES review(id) ON DELETE SET NULL;
CREATE INDEX idx_conversation_branches_review ON conversation_branches(review_id) WHERE review_id IS NOT NULL;
ALTER TABLE conversation_turns ADD COLUMN handled_by_review_id TEXT REFERENCES review(id) ON DELETE SET NULL;
ALTER TABLE conversation_provider_events ADD COLUMN review_id TEXT REFERENCES review(id) ON DELETE SET NULL;

CREATE TRIGGER review_conversation_branch_root_provider_update
AFTER UPDATE OF provider_conversation_id ON review
WHEN OLD.provider_conversation_id = '' AND NEW.provider_conversation_id <> ''
BEGIN
    UPDATE conversation_branches
    SET provider_conversation_id = NEW.provider_conversation_id
    WHERE parent_branch_id IS NULL AND provider_conversation_id = ''
      AND id IN (SELECT active_branch_id FROM conversations WHERE current_review_id = NEW.id);
END;

CREATE TRIGGER conversation_turns_branch_insert AFTER INSERT ON conversation_turns
WHEN NEW.branch_id = '' BEGIN
  UPDATE conversation_turns SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id) WHERE id = NEW.id;
END;
CREATE TRIGGER conversation_messages_branch_insert AFTER INSERT ON conversation_messages
WHEN NEW.branch_id = '' BEGIN
  UPDATE conversation_messages SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id) WHERE id = NEW.id;
END;
CREATE TRIGGER conversation_activities_branch_insert AFTER INSERT ON conversation_activities
WHEN NEW.branch_id = '' BEGIN
  UPDATE conversation_activities SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id) WHERE id = NEW.id;
END;
CREATE TRIGGER conversation_provider_events_branch_insert AFTER INSERT ON conversation_provider_events
WHEN NEW.branch_id = '' BEGIN
  UPDATE conversation_provider_events SET branch_id = (SELECT active_branch_id FROM conversations WHERE id = NEW.conversation_id) WHERE id = NEW.id;
END;
CREATE TRIGGER conversation_branch_root_provider_update
AFTER UPDATE OF provider_conversation_id ON sessions
WHEN OLD.provider_conversation_id = '' AND NEW.provider_conversation_id <> ''
BEGIN
  UPDATE conversation_branches SET provider_conversation_id = NEW.provider_conversation_id
  WHERE parent_branch_id IS NULL AND provider_conversation_id = ''
    AND id IN (SELECT active_branch_id FROM conversations WHERE current_session_id = NEW.id);
END;

CREATE TRIGGER review_conversation_title_cdc_update AFTER UPDATE OF provider_title ON conversations
WHEN OLD.provider_title <> NEW.provider_title AND NEW.current_review_id IS NOT NULL BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id, s.id, 'session_updated', json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', NEW.id, 'activity', s.activity_state, 'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at
  FROM review r JOIN sessions s ON s.id = r.session_id WHERE r.id = NEW.current_review_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert AFTER INSERT ON conversation_messages BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id, s.id, 'session_updated', json_object('id',s.id,'sessionId',s.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)), NEW.updated_at FROM conversations c JOIN sessions s ON s.id=c.current_session_id WHERE c.id=NEW.conversation_id
  UNION ALL SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'reviewId',r.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN review r ON r.id=c.current_review_id JOIN sessions s ON s.id=r.session_id WHERE c.id=NEW.conversation_id;
END;
CREATE TRIGGER conversation_messages_cdc_update AFTER UPDATE ON conversation_messages WHEN OLD.revision <> NEW.revision BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN sessions s ON s.id=c.current_session_id WHERE c.id=NEW.conversation_id
  UNION ALL SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'reviewId',r.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN review r ON r.id=c.current_review_id JOIN sessions s ON s.id=r.session_id WHERE c.id=NEW.conversation_id;
END;
CREATE TRIGGER conversation_activities_cdc_insert AFTER INSERT ON conversation_activities BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN sessions s ON s.id=c.current_session_id WHERE c.id=NEW.conversation_id
  UNION ALL SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'reviewId',r.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN review r ON r.id=c.current_review_id JOIN sessions s ON s.id=r.session_id WHERE c.id=NEW.conversation_id;
END;
CREATE TRIGGER conversation_activities_cdc_update AFTER UPDATE ON conversation_activities WHEN OLD.revision <> NEW.revision BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN sessions s ON s.id=c.current_session_id WHERE c.id=NEW.conversation_id
  UNION ALL SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'reviewId',r.id,'conversationId',c.id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),NEW.updated_at FROM conversations c JOIN review r ON r.id=c.current_review_id JOIN sessions s ON s.id=r.session_id WHERE c.id=NEW.conversation_id;
END;
CREATE TRIGGER conversation_turns_cdc_update AFTER UPDATE ON conversation_turns WHEN OLD.state <> NEW.state BEGIN
  INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
  SELECT s.project_id,s.id,'session_updated',json_object('id',s.id,'sessionId',s.id,'reviewId',NEW.handled_by_review_id,'conversationId',NEW.conversation_id,'activity',s.activity_state,'isTerminated',json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),COALESCE(NEW.completed_at,NEW.started_at,NEW.requested_at) FROM sessions s WHERE s.id=NEW.handled_by_session_id;
END;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
