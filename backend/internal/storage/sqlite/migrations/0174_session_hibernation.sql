-- +goose Up
ALTER TABLE sessions ADD COLUMN hibernated_at TIMESTAMP;

-- Keep the existing session trigger unchanged. This focused trigger sends the
-- same invalidation event only when the sleep marker changes, without moving
-- the session's user-visible updated_at/board recency.
-- +goose StatementBegin
CREATE TRIGGER sessions_hibernation_cdc_update
AFTER UPDATE OF hibernated_at ON sessions
WHEN OLD.hibernated_at IS NOT NEW.hibernated_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object('id', NEW.id, 'conversationId',
            (SELECT id FROM conversations WHERE current_session_id = NEW.id LIMIT 1)),
        CURRENT_TIMESTAMP);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER sessions_hibernation_cdc_update;
ALTER TABLE sessions DROP COLUMN hibernated_at;
