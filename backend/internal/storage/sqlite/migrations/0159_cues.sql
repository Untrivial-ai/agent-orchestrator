-- Summary: persist user-managed reusable quick actions (Cues) scoped to a project.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cues (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL CHECK (type IN ('command', 'agent')),
    command     TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP NOT NULL,
    UNIQUE (project_id, name)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS cues;
-- +goose StatementEnd
