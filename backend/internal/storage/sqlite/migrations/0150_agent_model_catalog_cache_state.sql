-- +goose Up
-- +goose StatementBegin
ALTER TABLE agent_model_catalog ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(metadata_json));
ALTER TABLE agent_model_catalog ADD COLUMN input_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_model_catalog ADD COLUMN last_success_at TIMESTAMP;
ALTER TABLE agent_model_catalog ADD COLUMN refresh_state TEXT NOT NULL DEFAULT 'idle'
    CHECK (refresh_state IN ('idle', 'queued', 'refreshing', 'error'));
ALTER TABLE agent_model_catalog ADD COLUMN refresh_error TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_model_catalog ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_model_catalog ADD COLUMN retry_at TIMESTAMP;
ALTER TABLE agent_model_catalog ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER agent_model_catalog_cdc_insert
AFTER INSERT ON agent_model_catalog
WHEN NEW.project_id <> '' AND EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NULL, 'session_updated',
        json_object('kind', 'model_catalog', 'agentId', NEW.agent_id, 'projectId', NEW.project_id),
        datetime('now'));
END;

CREATE TRIGGER agent_model_catalog_cdc_update
AFTER UPDATE ON agent_model_catalog
WHEN NEW.project_id <> '' AND EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NULL, 'session_updated',
        json_object('kind', 'model_catalog', 'agentId', NEW.agent_id, 'projectId', NEW.project_id),
        datetime('now'));
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_update;
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_insert;
ALTER TABLE agent_model_catalog DROP COLUMN generation;
ALTER TABLE agent_model_catalog DROP COLUMN retry_at;
ALTER TABLE agent_model_catalog DROP COLUMN retry_count;
ALTER TABLE agent_model_catalog DROP COLUMN refresh_error;
ALTER TABLE agent_model_catalog DROP COLUMN refresh_state;
ALTER TABLE agent_model_catalog DROP COLUMN last_success_at;
ALTER TABLE agent_model_catalog DROP COLUMN input_fingerprint;
ALTER TABLE agent_model_catalog DROP COLUMN metadata_json;
-- +goose StatementEnd
