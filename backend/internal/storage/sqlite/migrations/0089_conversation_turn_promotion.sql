-- Migration 0089: reserve queued turns while they are promoted into a running
-- provider turn. A reservation keeps queue drain from delivering the same user
-- message as a second turn while steering is in flight.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE conversation_turns ADD COLUMN promotion_started_at TIMESTAMP;
ALTER TABLE conversation_turns ADD COLUMN promoted_to_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Older supported SQLite versions cannot drop columns safely. Both additions are
-- nullable and are ignored by older binaries.
SELECT 1;
-- +goose StatementEnd
