-- +goose Up
ALTER TABLE conversation_messages ADD COLUMN client_payload_hash TEXT;

-- +goose Down
ALTER TABLE conversation_messages DROP COLUMN client_payload_hash;
