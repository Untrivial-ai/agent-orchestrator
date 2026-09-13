-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS schema_app_version (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL CHECK (version >= 0),
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO schema_app_version (id, version)
VALUES (1, 140)
ON CONFLICT(id) DO UPDATE SET
    version = excluded.version,
    updated_at = CURRENT_TIMESTAMP;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS schema_app_version;
-- +goose StatementEnd
