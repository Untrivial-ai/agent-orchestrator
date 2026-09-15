-- +goose Up
ALTER TABLE sessions ADD COLUMN native_checkpoint_evidence TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN native_checkpoint_evidence;
