-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN client_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN client_request_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN client_request_committed BOOLEAN NOT NULL DEFAULT FALSE CHECK (client_request_committed IN (0, 1));
CREATE UNIQUE INDEX idx_sessions_client_request ON sessions (client_request_id) WHERE client_request_id <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_sessions_client_request;
ALTER TABLE sessions DROP COLUMN client_request_committed;
ALTER TABLE sessions DROP COLUMN client_request_hash;
ALTER TABLE sessions DROP COLUMN client_request_id;
-- +goose StatementEnd
