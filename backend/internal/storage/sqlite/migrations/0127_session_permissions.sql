-- +goose Up
ALTER TABLE sessions ADD COLUMN session_permissions TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN session_permissions;
