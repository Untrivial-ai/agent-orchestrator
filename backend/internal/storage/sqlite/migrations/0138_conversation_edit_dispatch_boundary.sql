-- +goose Up
-- Existing reservations cannot prove whether provider work started. New
-- reservations explicitly start at zero and claim this boundary before I/O.
ALTER TABLE conversation_edit_deliveries ADD COLUMN provider_work_started INTEGER NOT NULL DEFAULT 1 CHECK (provider_work_started IN (0, 1));

-- +goose Down
ALTER TABLE conversation_edit_deliveries DROP COLUMN provider_work_started;
