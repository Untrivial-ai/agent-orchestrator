-- +goose Up
ALTER TABLE sessions
ADD COLUMN conversation_checkpoint_unsettled BOOLEAN NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN conversation_checkpoint_unsettled;
