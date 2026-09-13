-- +goose Up
CREATE TABLE agent_install_jobs_with_queue (
    target               TEXT PRIMARY KEY,
    status               TEXT NOT NULL CHECK (status IN ('queued', 'installing', 'verifying', 'succeeded', 'failed', 'unsupported', 'interrupted')),
    method               TEXT NOT NULL DEFAULT '',
    command              TEXT NOT NULL DEFAULT '',
    expected_destination TEXT NOT NULL DEFAULT '',
    output               TEXT NOT NULL DEFAULT '',
    error                TEXT NOT NULL DEFAULT '',
    started_at           TIMESTAMP NOT NULL,
    finished_at          TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL
);

INSERT INTO agent_install_jobs_with_queue SELECT * FROM agent_install_jobs;
DROP TABLE agent_install_jobs;
ALTER TABLE agent_install_jobs_with_queue RENAME TO agent_install_jobs;

-- +goose Down
UPDATE agent_install_jobs SET status = 'interrupted', error = 'Queued job interrupted by migration rollback.' WHERE status = 'queued';
CREATE TABLE agent_install_jobs_without_queue (
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

INSERT INTO agent_install_jobs_without_queue SELECT * FROM agent_install_jobs;
DROP TABLE agent_install_jobs;
ALTER TABLE agent_install_jobs_without_queue RENAME TO agent_install_jobs;
