-- +goose Up
ALTER TABLE agent_install_jobs ADD COLUMN version TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_install_jobs DROP COLUMN version;
