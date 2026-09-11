-- Summary: distinguish external-auth recovery operations and retain the triggering Chat queue.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE codex_account_switches
ADD COLUMN operation_kind TEXT NOT NULL DEFAULT 'account_switch'
CHECK (operation_kind IN ('account_switch', 'external_auth_recovery'));

ALTER TABLE codex_account_switch_sessions
ADD COLUMN retain_queued_turns BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE codex_account_switch_sessions DROP COLUMN retain_queued_turns;
ALTER TABLE codex_account_switches DROP COLUMN operation_kind;
-- +goose StatementEnd
