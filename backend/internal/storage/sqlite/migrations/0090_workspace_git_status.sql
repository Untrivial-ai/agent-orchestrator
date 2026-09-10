-- Track whether a workspace child is a usable git repository (ready) or a
-- plain folder awaiting `git init` (needs_init).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE workspace_repos
    ADD COLUMN git_status TEXT NOT NULL DEFAULT 'ready'
    CHECK (git_status IN ('ready', 'needs_init'));
-- +goose StatementEnd

-- +goose Down
-- SQLite < 3.35 cannot DROP COLUMN; recreate the table without git_status.
-- +goose StatementBegin
CREATE TABLE workspace_repos_new (
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    relative_path   TEXT NOT NULL,
    repo_origin_url TEXT NOT NULL DEFAULT '',
    default_branch  TEXT NOT NULL DEFAULT '',
    registered_at   TIMESTAMP NOT NULL,
    PRIMARY KEY (project_id, name),
    UNIQUE (project_id, relative_path)
);
INSERT INTO workspace_repos_new (project_id, name, relative_path, repo_origin_url, default_branch, registered_at)
SELECT project_id, name, relative_path, repo_origin_url, default_branch, registered_at FROM workspace_repos;
DROP TABLE workspace_repos;
ALTER TABLE workspace_repos_new RENAME TO workspace_repos;
-- +goose StatementEnd
