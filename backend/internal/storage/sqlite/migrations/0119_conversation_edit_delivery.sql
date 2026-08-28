-- +goose Up
-- +goose StatementBegin
-- Edit delivery changes both the provider's conversation branch and AO's active
-- lineage. Reserve the renderer's delivery handle before either boundary so a
-- lost response can replay a known result without asking the provider twice.
CREATE TABLE conversation_edit_deliveries (
    conversation_id    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    client_message_id  TEXT NOT NULL,
    request_json       TEXT NOT NULL,
    state              TEXT NOT NULL CHECK (state IN ('reserved', 'accepted', 'rejected')),
    source_branch_id   TEXT NOT NULL DEFAULT '',
    active_branch_id   TEXT NOT NULL DEFAULT '',
    turn_id            TEXT NOT NULL DEFAULT '',
    handled_by_session_id TEXT NOT NULL DEFAULT '',
    provider_turn_id   TEXT NOT NULL DEFAULT '',
    turn_state         TEXT NOT NULL DEFAULT '',
    turn_requested_at  TIMESTAMP,
    rejection_kind     TEXT NOT NULL DEFAULT '',
    rejection_message  TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL,
    settled_at          TIMESTAMP,
    PRIMARY KEY (conversation_id, client_message_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE conversation_edit_deliveries;
-- +goose StatementEnd
