-- +goose Up
CREATE TABLE test_worker_legs (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id),
 base_run_id TEXT NOT NULL REFERENCES test_runs(id),
 head_run_id TEXT NOT NULL REFERENCES test_runs(id),
 timeout_seconds INTEGER NOT NULL CHECK(timeout_seconds BETWEEN 1 AND 7200),
 CHECK(base_run_id <> head_run_id)
);

-- +goose Down
DROP TABLE test_worker_legs;
