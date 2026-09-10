-- +goose Up
-- +goose StatementBegin

-- Phase 2.4: persist resolved AgentRole.SystemPrompt snapshot so Restore
-- can replay it without re-querying the agent_roles table.
ALTER TABLE sessions ADD COLUMN additional_system_prompt TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE sessions DROP COLUMN additional_system_prompt;

-- +goose StatementEnd
