-- +goose Up

-- Persist the provider reasoning effort selected from Cloud ChatUI so a TUI
-- handoff and later headless turns use the same setting.
ALTER TABLE ao_sessions ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE ao_sessions DROP COLUMN IF EXISTS reasoning_effort;
