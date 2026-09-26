-- +goose Up
ALTER TABLE session_worktrees ADD COLUMN creation_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session_worktrees DROP COLUMN creation_sha;
