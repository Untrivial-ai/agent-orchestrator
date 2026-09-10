-- +goose Up
ALTER TABLE session_worktrees ADD COLUMN base_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session_worktrees DROP COLUMN base_ref;
