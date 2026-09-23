-- +goose Up
ALTER TABLE workspace_repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workspace_repos DROP COLUMN default_branch;
