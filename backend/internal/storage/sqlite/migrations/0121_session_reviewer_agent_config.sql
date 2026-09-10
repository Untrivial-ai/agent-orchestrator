-- +goose Up
ALTER TABLE sessions ADD COLUMN reviewer_agent_config TEXT NOT NULL DEFAULT '';

-- +goose Down
