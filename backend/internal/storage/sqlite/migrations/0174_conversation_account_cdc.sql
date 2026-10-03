-- +goose Up
-- Auth recovery may change only account facts (including lazy legacy repair).
-- Invalidate mounted Chat views even when no timeline row or turn changes.
-- +goose StatementBegin
CREATE TRIGGER conversation_account_cdc_update
AFTER UPDATE OF account_json ON conversations
WHEN OLD.account_json IS NOT NEW.account_json
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM sessions s WHERE s.id = NEW.current_session_id
    UNION ALL
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'reviewId', r.id, 'conversationId', NEW.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM review r JOIN sessions s ON s.id = r.session_id WHERE r.id = NEW.current_review_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER conversation_account_cdc_update;
