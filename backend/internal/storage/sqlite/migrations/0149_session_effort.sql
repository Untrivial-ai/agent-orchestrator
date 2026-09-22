-- +goose Up
ALTER TABLE sessions ADD COLUMN session_effort TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN session_effort;
