-- +goose Up
-- +goose StatementBegin
CREATE TABLE automations (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    display_name  TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 120),
    prompt        TEXT NOT NULL CHECK (length(prompt) BETWEEN 1 AND 4096),
    kind          TEXT NOT NULL CHECK (kind IN ('worker', 'orchestrator')),
    harness       TEXT NOT NULL DEFAULT '',
    rrule_text    TEXT NOT NULL,
    timezone      TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE CHECK (enabled IN (0, 1)),
    next_run_at   TIMESTAMP NOT NULL,
    last_run_at   TIMESTAMP,
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL
);

CREATE INDEX idx_automations_due
    ON automations (enabled, next_run_at);
CREATE INDEX idx_automations_project
    ON automations (project_id, created_at, id);

CREATE TABLE automation_runs (
    id               TEXT PRIMARY KEY,
    automation_id    TEXT NOT NULL REFERENCES automations (id) ON DELETE CASCADE,
    scheduled_for    TIMESTAMP NOT NULL,
    session_id       TEXT REFERENCES sessions (id) ON DELETE SET NULL,
    status           TEXT NOT NULL CHECK (status IN ('pending', 'spawning', 'running', 'completed', 'failed')),
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claimed_at       TIMESTAMP,
    lease_expires_at TIMESTAMP,
    started_at       TIMESTAMP,
    finished_at      TIMESTAMP,
    error_message    TEXT,
    created_at       TIMESTAMP NOT NULL,
    updated_at       TIMESTAMP NOT NULL,
    UNIQUE (automation_id, scheduled_for)
);

CREATE INDEX idx_automation_runs_dispatch
    ON automation_runs (status, lease_expires_at, scheduled_for);
CREATE INDEX idx_automation_runs_history
    ON automation_runs (automation_id, scheduled_for DESC, id DESC);

ALTER TABLE sessions
    ADD COLUMN automation_run_id TEXT REFERENCES automation_runs (id) ON DELETE SET NULL;
ALTER TABLE sessions
    ADD COLUMN automation_launch_completed BOOLEAN NOT NULL DEFAULT FALSE CHECK (automation_launch_completed IN (0, 1));
CREATE UNIQUE INDEX idx_sessions_automation_run
    ON sessions (automation_run_id)
    WHERE automation_run_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_sessions_automation_run;
ALTER TABLE sessions DROP COLUMN automation_launch_completed;
ALTER TABLE sessions DROP COLUMN automation_run_id;
DROP TABLE automation_runs;
DROP TABLE automations;
-- +goose StatementEnd
