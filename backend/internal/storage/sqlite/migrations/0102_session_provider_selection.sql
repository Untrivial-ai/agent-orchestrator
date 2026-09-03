-- +goose Up
-- +goose StatementBegin
-- Persist the exact Provider selection used by a TUI session so native
-- restore/resume can resolve the same DPAPI-backed secret after any store read
-- or daemon restart. Display names are snapshots only; ids remain authoritative.
ALTER TABLE sessions ADD COLUMN provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN provider_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN provider_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN provider_model_name TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN provider_model_name;
ALTER TABLE sessions DROP COLUMN provider_display_name;
ALTER TABLE sessions DROP COLUMN provider_model_id;
ALTER TABLE sessions DROP COLUMN provider_id;
-- +goose StatementEnd
