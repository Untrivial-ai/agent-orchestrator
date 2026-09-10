-- +goose Up
CREATE TABLE agent_install_jobs (
    target               TEXT PRIMARY KEY,
    status               TEXT NOT NULL CHECK (status IN ('installing', 'verifying', 'succeeded', 'failed', 'unsupported', 'interrupted')),
    method               TEXT NOT NULL DEFAULT '',
    command              TEXT NOT NULL DEFAULT '',
    expected_destination TEXT NOT NULL DEFAULT '',
    output               TEXT NOT NULL DEFAULT '',
    error                TEXT NOT NULL DEFAULT '',
    started_at           TIMESTAMP NOT NULL,
    finished_at          TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL
);

-- Install-job polling is a dedicated API concern. It deliberately does not
-- emit session CDC events into change_log.

-- +goose Down
DROP TABLE agent_install_jobs;
