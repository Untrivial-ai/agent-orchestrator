-- Tie the TUI native resume handle to the runtime generation whose hook
-- reported it. A resume handle intentionally survives relaunch, but it must
-- not be used for an interface handoff until the new visible TUI confirms it.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN agent_session_id_launch_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- Do not backfill legacy rows. first_signal_at can also be written by terminal
-- surface reconciliation, so it cannot prove that a provider hook associated
-- agent_session_id with the current launch. The next provider hook safely fills
-- this field; until then the existing id remains only a resume hint.

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN agent_session_id_launch_id;
-- +goose StatementEnd
