-- +goose Up
-- +goose StatementBegin
-- The cloud offering becomes a user preference: a Developer Mode-gated toggle
-- in Settings, persisted daemon-side like every preference so desktop, mobile,
-- and headless spawns resolve the same answer. Off by default: enabling cloud
-- is always a deliberate act.
ALTER TABLE app_settings
    ADD COLUMN cloud_offering INTEGER NOT NULL DEFAULT 0
        CHECK (cloud_offering IN (0, 1));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE app_settings DROP COLUMN cloud_offering;
-- +goose StatementEnd
