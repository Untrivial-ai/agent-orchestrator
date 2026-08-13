-- +goose Up
ALTER TABLE workspace_repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT 'main';

-- +goose Down
ALTER TABLE workspace_repos DROP COLUMN default_branch;
