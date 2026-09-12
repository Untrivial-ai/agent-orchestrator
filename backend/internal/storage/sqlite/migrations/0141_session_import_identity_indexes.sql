-- +goose Up
CREATE INDEX sessions_import_conversation ON sessions(harness, provider_conversation_id) WHERE is_terminated = 0;
CREATE INDEX sessions_import_agent ON sessions(harness, agent_session_id) WHERE is_terminated = 0;

-- +goose Down
DROP INDEX sessions_import_agent;
DROP INDEX sessions_import_conversation;
