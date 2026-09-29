-- Cloud sessions carry a per-session coding-agent model so a task launches with
-- the model the user selected in the composer, not the harness default. Nullable
-- via a '' default: existing rows and callers that omit it keep the harness
-- default, so this is backward compatible.
-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ao_sessions
    DROP COLUMN model;
