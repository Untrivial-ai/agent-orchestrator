-- +goose Up
-- Summary-only writes must invalidate the board without making every provider
-- checkpoint a workspace-wide event.
-- +goose StatementBegin
CREATE TRIGGER sessions_card_summary_cdc
AFTER UPDATE OF latest_assistant_update ON sessions
WHEN NEW.latest_assistant_update <> OLD.latest_assistant_update
    AND NEW.latest_assistant_update LIKE 'ao-card-summary:%'
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated', json_object('id', NEW.id), NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS sessions_card_summary_cdc;
