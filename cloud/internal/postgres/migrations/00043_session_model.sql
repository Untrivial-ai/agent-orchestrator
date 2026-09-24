-- +goose Up

-- Persist the provider model selected from Cloud ChatUI so the next TUI
-- controller starts with the same model.
ALTER TABLE ao_sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE ao_sessions DROP COLUMN IF EXISTS model;
