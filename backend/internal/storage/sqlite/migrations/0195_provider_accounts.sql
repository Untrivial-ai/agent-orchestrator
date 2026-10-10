-- Summary: the accounts AO manages and their session routes, as one document. No credentials.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS provider_account_state (id INTEGER PRIMARY KEY CHECK (id = 1), facts TEXT NOT NULL);
INSERT INTO provider_account_state (id, facts) VALUES (1, '{"accounts":[],"routes":[]}') ON CONFLICT DO NOTHING;
CREATE TRIGGER IF NOT EXISTS provider_account_routes_cdc
AFTER UPDATE OF facts ON provider_account_state
WHEN NEW.facts <> OLD.facts
BEGIN
 INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
 SELECT s.project_id, s.id, 'session_updated', json_object('id',s.id), datetime('now')
 FROM sessions s WHERE s.id IN (
 SELECT json_extract(value,'$.session_id') FROM json_each(NEW.facts,'$.routes')
 UNION SELECT json_extract(value,'$.session_id') FROM json_each(OLD.facts,'$.routes')
 );
END;
-- +goose StatementEnd
-- +goose Down
DROP TABLE provider_account_state;
