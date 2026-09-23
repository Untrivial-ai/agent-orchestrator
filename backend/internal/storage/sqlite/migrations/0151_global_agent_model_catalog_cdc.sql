-- +goose Up
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_update;
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_insert;

CREATE TRIGGER agent_model_catalog_cdc_insert
AFTER INSERT ON agent_model_catalog
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT projects.id, NULL, 'session_updated',
        json_object('kind', 'model_catalog', 'agentId', NEW.agent_id, 'projectId', projects.id),
        datetime('now')
    FROM projects
    WHERE (NEW.project_id = '' AND projects.archived_at IS NULL)
       OR projects.id = NEW.project_id;
END;

CREATE TRIGGER agent_model_catalog_cdc_update
AFTER UPDATE ON agent_model_catalog
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT projects.id, NULL, 'session_updated',
        json_object('kind', 'model_catalog', 'agentId', NEW.agent_id, 'projectId', projects.id),
        datetime('now')
    FROM projects
    WHERE (NEW.project_id = '' AND projects.archived_at IS NULL)
       OR projects.id = NEW.project_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_update;
DROP TRIGGER IF EXISTS agent_model_catalog_cdc_insert;

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
