-- +goose Up
ALTER TABLE sessions ADD COLUMN latest_assistant_update_at TIMESTAMP;
ALTER TABLE sessions ADD COLUMN native_identity_observed_at TIMESTAMP;
ALTER TABLE conversation_branches ADD COLUMN provider_ids_scoped INTEGER NOT NULL DEFAULT 0 CHECK (provider_ids_scoped IN (0, 1));

-- +goose Down
ALTER TABLE sessions DROP COLUMN latest_assistant_update_at;
ALTER TABLE sessions DROP COLUMN native_identity_observed_at;
ALTER TABLE conversation_branches DROP COLUMN provider_ids_scoped;
