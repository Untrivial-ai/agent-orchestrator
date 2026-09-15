-- Summary: persist whether a Codex account switch should restart running AO controllers.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE codex_account_switches
ADD COLUMN restart_running_sessions BOOLEAN NOT NULL DEFAULT TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE codex_account_switches
DROP COLUMN restart_running_sessions;
-- +goose StatementEnd
