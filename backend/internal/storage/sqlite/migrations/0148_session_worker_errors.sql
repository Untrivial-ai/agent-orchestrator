-- +goose Up
-- session_worker_errors is the worker watchdog's durable error log: one row
-- per observed worker failure carrying a bounded provider-error excerpt and
-- its timestamp. Chat provider-turn failures are recorded in v1; the source
-- column leaves room for later observers (hook-reported provider errors,
-- workspace failures) without a schema change. The watchdog derives
-- needsAttention at read time from these rows plus activity recency; the
-- verdict itself is never stored. Rows for rolled-back turns are excluded at
-- read time (rollback discards that history provider-side), and writers prune
-- each session to the newest rows so the log stays bounded.
CREATE TABLE session_worker_errors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    turn_id TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL,
    occurred_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_session_worker_errors_session
    ON session_worker_errors(session_id, occurred_at DESC, id DESC);

-- +goose Down
DROP TABLE session_worker_errors;
