-- +goose Up
-- +goose StatementBegin
-- Commit the caller's receipt with the queued message change so retries remain
-- safe after dispatch, controller teardown, or a lost HTTP response.
CREATE TABLE conversation_queued_edit_deliveries (
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    client_message_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (conversation_id, client_message_id)
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE conversation_queued_edit_deliveries;
