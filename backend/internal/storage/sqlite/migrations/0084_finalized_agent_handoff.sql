-- Retain the exact AO-finalized context delivered during an agent switch.
-- This is separate from the optional source-authored semantic report because
-- fallback-only switches still need a durable restore source.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE agent_switches ADD COLUMN final_handoff_path TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE agent_switches ADD COLUMN final_handoff_hash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_switches DROP COLUMN final_handoff_hash;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE agent_switches DROP COLUMN final_handoff_path;
-- +goose StatementEnd
