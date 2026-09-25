-- +goose Up
-- A reconnect watchdog may durably fail its root turn before the shared provider
-- host can be terminated. Keep the outstanding destructive obligation on that
-- exact turn so a daemon restart can retry it without relying on the turn still
-- being running.
ALTER TABLE conversation_turns
ADD COLUMN provider_host_termination_pending INTEGER NOT NULL DEFAULT 0
    CHECK (provider_host_termination_pending IN (0, 1));

-- +goose Down
ALTER TABLE conversation_turns DROP COLUMN provider_host_termination_pending;
