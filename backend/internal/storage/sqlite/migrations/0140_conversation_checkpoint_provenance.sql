-- +goose Up
-- +goose StatementBegin
-- Existing hook-derived checkpoint text predates event, owner-generation, and main-turn
-- scoping. Keep it available for strict compatibility, but mark it legacy so
-- an explicit provider-history recovery can ignore only those untrusted text
-- dimensions. New observations advance the state machine with durable owner
-- provenance.
ALTER TABLE sessions
ADD COLUMN conversation_checkpoint_state TEXT NOT NULL DEFAULT 'legacy'
    CHECK (conversation_checkpoint_state IN ('legacy', 'empty', 'coordination', 'prompt', 'complete'));
ALTER TABLE sessions
ADD COLUMN conversation_checkpoint_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions
ADD COLUMN conversation_checkpoint_native_id TEXT NOT NULL DEFAULT '';

-- Provider-history consent belongs to the durable handoff attempt. Persisting
-- it prevents a daemon restart between source stop and target admission from
-- silently returning to strict semantics (or broadening the consent).
ALTER TABLE session_interface_transitions
ADD COLUMN history_policy TEXT NOT NULL DEFAULT 'strict'
    CHECK (history_policy IN ('strict', 'provider_history'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE session_interface_transitions DROP COLUMN history_policy;
ALTER TABLE sessions DROP COLUMN conversation_checkpoint_native_id;
ALTER TABLE sessions DROP COLUMN conversation_checkpoint_generation;
ALTER TABLE sessions DROP COLUMN conversation_checkpoint_state;
-- +goose StatementEnd
