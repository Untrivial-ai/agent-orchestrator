-- +goose Up
ALTER TABLE sessions ADD COLUMN terminate_on_turn_complete BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE sessions DROP COLUMN terminate_on_turn_complete;
