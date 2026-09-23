-- +goose Up

-- A queued turn is created from one specific user message. Recording the
-- sender's share-grant cap (if any) on the turn itself — rather than only
-- ever reading the session's own mode/denied_commands at claim time — is
-- what lets a shared, capped collaborator's message actually constrain the
-- agent turn it triggers, not just the API surfaces (terminal ticket issue,
-- workspace write) that already enforced it. See ClaimWorkerTurn and
-- appendUserMessage in internal/postgres.
ALTER TABLE ao_turns
    ADD COLUMN mode_cap TEXT CHECK (mode_cap IN ('read-only', 'standard', 'trusted')),
    ADD COLUMN denied_commands TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE ao_turns
    DROP COLUMN mode_cap,
    DROP COLUMN denied_commands;
