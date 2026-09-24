-- Name the timeline row a conversation change touched.
--
-- The payload stays an invalidation signal: sequence and revision only, never
-- chat prose. A client that already holds that row can refresh the live page.
-- A client that does not understand the new fields still sees conversationId
-- and refreshes the conversation the way it did before.
--
-- Both the session row and the reviewer row from 0149 stay. This migration
-- only adds the sequence fields to those payloads.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;

CREATE TRIGGER conversation_activities_cdc_insert
AFTER INSERT ON conversation_activities
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_update
AFTER UPDATE ON conversation_activities
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert
AFTER INSERT ON conversation_messages
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_update
AFTER UPDATE ON conversation_messages
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'itemSequence', NEW.sequence, 'itemRevision', NEW.revision,
                       'headSequence', c.latest_sequence,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;

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
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
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
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert
AFTER INSERT ON conversation_messages
BEGIN
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
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_update
AFTER UPDATE ON conversation_messages
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
    WHERE c.id = NEW.conversation_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN review r ON r.id = c.current_review_id
    JOIN sessions s ON s.id = r.session_id
    WHERE c.id = NEW.conversation_id;
END;
-- +goose StatementEnd
