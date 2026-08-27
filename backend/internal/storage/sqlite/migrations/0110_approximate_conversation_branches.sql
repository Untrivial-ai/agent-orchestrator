-- +goose Up
ALTER TABLE conversation_branches ADD COLUMN strategy TEXT NOT NULL DEFAULT 'native';
ALTER TABLE conversation_branches ADD COLUMN replay_cutoff_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_branches ADD COLUMN replay_truncated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_branches ADD COLUMN provider_scope_id TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite branch metadata is forward-only on supported versions.
