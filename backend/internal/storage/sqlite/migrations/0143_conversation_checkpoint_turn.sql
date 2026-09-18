-- +goose Up
ALTER TABLE sessions
ADD COLUMN conversation_checkpoint_turn_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN conversation_checkpoint_turn_id;
