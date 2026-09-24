-- +goose Up
-- +goose StatementBegin
CREATE TABLE research_runs (
    id TEXT PRIMARY KEY,
    parent_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    prompt TEXT NOT NULL CHECK (length(trim(prompt)) BETWEEN 1 AND 16384),
    harness TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    permissions TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled', 'interrupted')),
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    started_at TIMESTAMP,
    finished_at TIMESTAMP
);
CREATE UNIQUE INDEX idx_research_one_active_parent ON research_runs(parent_session_id)
    WHERE status IN ('queued', 'running');
CREATE INDEX idx_research_parent_history ON research_runs(parent_session_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_research_parent_history;
DROP INDEX IF EXISTS idx_research_one_active_parent;
DROP TABLE IF EXISTS research_runs;
-- +goose StatementEnd
