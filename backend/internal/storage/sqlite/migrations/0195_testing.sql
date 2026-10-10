-- +goose Up
-- +goose StatementBegin
-- Preserve existing CDC triggers while extending its closed event vocabulary.
-- legacy_alter_table skips validation while the replacement table is renamed.
PRAGMA legacy_alter_table = ON;
CREATE TABLE change_log_testing (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT REFERENCES projects(id),
 session_id TEXT REFERENCES sessions(id),
 event_type TEXT NOT NULL CHECK(event_type IN ('session_created', 'session_updated', 'pr_created', 'pr_updated', 'pr_check_recorded', 'pr_session_changed', 'pr_review_thread_added', 'pr_review_thread_resolved', 'review_run_created', 'review_run_updated', 'testing_updated')),
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO change_log_testing SELECT * FROM change_log;
UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name='change_log')) WHERE name='change_log_testing';
DROP TABLE change_log;
ALTER TABLE change_log_testing RENAME TO change_log;
CREATE INDEX idx_change_log_project ON change_log(project_id, seq);
CREATE INDEX idx_change_log_created_at_seq ON change_log(created_at, seq);
PRAGMA legacy_alter_table = OFF;
CREATE TABLE test_runs (
 id TEXT PRIMARY KEY,
 linked_run_id TEXT REFERENCES test_runs(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 issue_url TEXT NOT NULL,
 issue_snapshot TEXT NOT NULL CHECK(json_valid(issue_snapshot)),
 commit_sha TEXT NOT NULL,
 recipe_snapshot TEXT NOT NULL CHECK(json_valid(recipe_snapshot)),
 requester TEXT NOT NULL,
 worker_session_id TEXT REFERENCES sessions(id),
 report_evidence_id TEXT,
 created_at TIMESTAMP NOT NULL
);
CREATE TABLE test_attempts (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES test_runs(id),
 number INTEGER NOT NULL CHECK(number > 0),
 target_identity TEXT CHECK(target_identity IS NULL OR json_valid(target_identity)),
 lease_generation INTEGER NOT NULL DEFAULT 1 CHECK(lease_generation > 0),
 phase TEXT NOT NULL CHECK(phase IN ('starting','active','finished')),
 deadline TIMESTAMP NOT NULL,
 outcome TEXT CHECK(outcome IN ('reproduced','not_reproduced','needs_information','environment_blocked','partial','cancelled')),
 cleanup_state TEXT NOT NULL CHECK(cleanup_state IN ('pending','running','complete','failed')),
 recording_gap TEXT NOT NULL DEFAULT '',
 cancelled_at TIMESTAMP,
 created_at TIMESTAMP NOT NULL,
 finished_at TIMESTAMP,
 UNIQUE(run_id, number)
);
CREATE UNIQUE INDEX test_attempts_one_live ON test_attempts(run_id) WHERE phase <> 'finished';
CREATE TABLE session_test_tools (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id),
 attempt_id TEXT NOT NULL REFERENCES test_attempts(id),
 profile_id TEXT NOT NULL CHECK(profile_id = 'ao-native-test-v1')
);
CREATE TRIGGER test_runs_insert_cdc AFTER INSERT ON test_runs
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES (NEW.project_id, NEW.worker_session_id, 'testing_updated', json_object('runId', NEW.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER test_runs_update_cdc AFTER UPDATE ON test_runs
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES (NEW.project_id, NEW.worker_session_id, 'testing_updated', json_object('runId', NEW.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER test_runs_delete_cdc AFTER DELETE ON test_runs
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES (OLD.project_id, OLD.worker_session_id, 'testing_updated', json_object('runId', OLD.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER test_attempts_insert_cdc AFTER INSERT ON test_attempts
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM test_runs WHERE id=NEW.run_id), (SELECT worker_session_id FROM test_runs WHERE id=NEW.run_id), 'testing_updated', json_object('runId', NEW.run_id, 'attemptId', NEW.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER test_attempts_update_cdc AFTER UPDATE ON test_attempts
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM test_runs WHERE id=NEW.run_id), (SELECT worker_session_id FROM test_runs WHERE id=NEW.run_id), 'testing_updated', json_object('runId', NEW.run_id, 'attemptId', NEW.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER test_attempts_delete_cdc AFTER DELETE ON test_attempts
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM test_runs WHERE id=OLD.run_id), (SELECT worker_session_id FROM test_runs WHERE id=OLD.run_id), 'testing_updated', json_object('runId', OLD.run_id, 'attemptId', OLD.id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER session_test_tools_insert_cdc AFTER INSERT ON session_test_tools
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM sessions WHERE id=NEW.session_id), NEW.session_id, 'testing_updated', json_object('sessionId', NEW.session_id, 'attemptId', NEW.attempt_id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER session_test_tools_update_cdc AFTER UPDATE ON session_test_tools
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM sessions WHERE id=NEW.session_id), NEW.session_id, 'testing_updated', json_object('sessionId', NEW.session_id, 'attemptId', NEW.attempt_id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER session_test_tools_delete_cdc AFTER DELETE ON session_test_tools
BEGIN
 INSERT INTO change_log(project_id, session_id, event_type, payload, created_at)
 VALUES ((SELECT project_id FROM sessions WHERE id=OLD.session_id), OLD.session_id, 'testing_updated', json_object('sessionId', OLD.session_id, 'attemptId', OLD.attempt_id), strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TABLE session_test_tools;
DROP TABLE test_attempts;
DROP TABLE test_runs;
-- Preserve existing CDC triggers while extending its closed event vocabulary.
-- legacy_alter_table skips validation while the replacement table is renamed.
PRAGMA legacy_alter_table = ON;
CREATE TABLE change_log_testing (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT REFERENCES projects(id),
 session_id TEXT REFERENCES sessions(id),
 event_type TEXT NOT NULL CHECK(event_type IN ('session_created', 'session_updated', 'pr_created', 'pr_updated', 'pr_check_recorded', 'pr_session_changed', 'pr_review_thread_added', 'pr_review_thread_resolved', 'review_run_created', 'review_run_updated')),
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO change_log_testing SELECT * FROM change_log WHERE event_type <> 'testing_updated';
UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name='change_log')) WHERE name='change_log_testing';
DROP TABLE change_log;
ALTER TABLE change_log_testing RENAME TO change_log;
CREATE INDEX idx_change_log_project ON change_log(project_id, seq);
CREATE INDEX idx_change_log_created_at_seq ON change_log(created_at, seq);
PRAGMA legacy_alter_table = OFF;
-- +goose StatementEnd
