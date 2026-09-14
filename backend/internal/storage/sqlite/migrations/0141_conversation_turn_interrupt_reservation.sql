-- +goose Up
-- +goose StatementBegin
-- Stop releases the controller's dispatch lock while the provider interrupt is
-- in flight. The conversation marker is the global dispatch fence, including
-- when the confirmed queue was empty; its session owner lets restart recovery
-- settle an orphaned request. Per-turn markers retain the exact confirmed scope.
ALTER TABLE conversations
    ADD COLUMN interrupt_reservation_id TEXT;
-- Keep the default NO ACTION deletion rule: owner cleanup must settle the exact
-- confirmed scope before deleting the session, never erase the recovery key.
ALTER TABLE conversations
    ADD COLUMN interrupt_reservation_session_id TEXT REFERENCES sessions(id);
ALTER TABLE conversations
    ADD COLUMN interrupt_provider_turn_id TEXT;
ALTER TABLE conversation_turns
    ADD COLUMN interrupt_reservation_id TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE conversation_turns DROP COLUMN interrupt_reservation_id;
ALTER TABLE conversations DROP COLUMN interrupt_provider_turn_id;
ALTER TABLE conversations DROP COLUMN interrupt_reservation_session_id;
ALTER TABLE conversations DROP COLUMN interrupt_reservation_id;
-- +goose StatementEnd
