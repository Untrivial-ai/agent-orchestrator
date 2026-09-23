-- +goose Up
ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN effort;
