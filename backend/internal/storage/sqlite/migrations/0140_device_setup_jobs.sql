-- +goose Up
CREATE TABLE device_setup_jobs (
    platform          TEXT PRIMARY KEY CHECK (platform IN ('ios', 'android')),
    state             TEXT NOT NULL,
    stage             TEXT NOT NULL DEFAULT '',
    message           TEXT NOT NULL DEFAULT '',
    progress          INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    downloaded_bytes  INTEGER NOT NULL DEFAULT 0,
    total_bytes       INTEGER NOT NULL DEFAULT 0,
    required_bytes    INTEGER NOT NULL DEFAULT 0,
    available_bytes   INTEGER NOT NULL DEFAULT 0,
    license_url       TEXT NOT NULL DEFAULT '',
    license_accepted  INTEGER NOT NULL DEFAULT 0,
    action_url        TEXT NOT NULL DEFAULT '',
    error_code        TEXT NOT NULL DEFAULT '',
    error             TEXT NOT NULL DEFAULT '',
    installed_version TEXT NOT NULL DEFAULT '',
    started_at        TIMESTAMP NOT NULL,
    finished_at       TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL
);

-- Setup polling is deliberately independent of session CDC events.

-- +goose Down
DROP TABLE device_setup_jobs;
