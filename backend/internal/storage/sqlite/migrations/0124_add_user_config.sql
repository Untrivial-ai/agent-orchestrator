-- The user-scope agent config: the lowest-precedence config scope, above projects.
-- A single nullable JSON column holds the typed domain.AgentConfig (model and
-- permissions today) that every project inherits field-by-field unless it overrides
-- a field. NULL means unset; a non-NULL value is a JSON object. One row only — the
-- id=1 CHECK enforces the singleton at the schema level. See ADR 0002.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE user_config (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    config TEXT
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE user_config;
-- +goose StatementEnd
