-- +goose Up
-- +goose StatementBegin
-- Retry lineage is a durable turn fact. Keeping it on the turn prevents a
-- caller-controlled client_message_id from impersonating or consuming a retry.
ALTER TABLE conversation_turns
    ADD COLUMN retry_of_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT;

-- One source turn owns at most one retry attempt. A further deliberate attempt
-- retries the failed child turn, producing a new link in the chain.
CREATE UNIQUE INDEX idx_conversation_turns_retry_source
    ON conversation_turns(conversation_id, retry_of_turn_id)
    WHERE retry_of_turn_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_conversation_turns_retry_source;
ALTER TABLE conversation_turns DROP COLUMN retry_of_turn_id;
-- +goose StatementEnd
