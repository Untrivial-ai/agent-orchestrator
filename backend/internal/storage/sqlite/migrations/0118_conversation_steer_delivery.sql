-- +goose Up
-- +goose StatementBegin
-- Steer delivery is provider I/O with no provider-side idempotency guarantee. Reserve
-- its client handle before dispatch so a daemon crash can leave an honest
-- "unknown" result instead of sending the same guidance twice after restart.
CREATE TABLE conversation_steer_deliveries (
    conversation_id    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    client_message_id  TEXT NOT NULL,
    request_json       TEXT NOT NULL,
    state              TEXT NOT NULL CHECK (state IN ('reserved', 'accepted', 'rejected')),
    provider_turn_id   TEXT NOT NULL DEFAULT '',
    activity_id        TEXT NOT NULL DEFAULT '',
    rejection_kind     TEXT NOT NULL DEFAULT '',
    rejection_message  TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL,
    settled_at          TIMESTAMP,
    PRIMARY KEY (conversation_id, client_message_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE conversation_steer_deliveries;
-- +goose StatementEnd
