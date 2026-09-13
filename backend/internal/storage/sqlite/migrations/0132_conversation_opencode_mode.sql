-- +goose Up
ALTER TABLE conversations ADD COLUMN opencode_mode TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE conversations DROP COLUMN opencode_mode;
