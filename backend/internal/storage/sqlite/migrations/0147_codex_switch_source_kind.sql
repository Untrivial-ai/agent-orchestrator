-- Summary: persist what occupied the device Codex credential before a switch.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE codex_account_switches
ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'managed'
CHECK (source_kind IN ('managed', 'device', 'none'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE codex_account_switches DROP COLUMN source_kind;
-- +goose StatementEnd
