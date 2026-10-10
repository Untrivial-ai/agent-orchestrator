-- name: CreateTestRun :exec
INSERT INTO test_runs(id, linked_run_id, project_id, issue_url, issue_snapshot, commit_sha, recipe_snapshot, requester, worker_session_id, report_evidence_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
-- name: GetTestRun :one
SELECT * FROM test_runs WHERE id = ?;
-- name: SetTestRunWorker :execrows
UPDATE test_runs SET worker_session_id = ? WHERE id = ?;
-- name: SetTestRunReport :execrows
UPDATE test_runs SET report_evidence_id = ? WHERE id = ?;
-- name: CreateTestAttempt :one
INSERT INTO test_attempts(id, run_id, number, phase, deadline, cleanup_state, recording_gap, created_at)
VALUES (sqlc.arg(id), sqlc.arg(run_id), (SELECT COALESCE(MAX(number),0)+1 FROM test_attempts WHERE run_id=sqlc.arg(run_id)), 'starting', sqlc.arg(deadline), 'pending', sqlc.arg(recording_gap), sqlc.arg(created_at)) RETURNING *;
-- name: GetTestAttempt :one
SELECT * FROM test_attempts WHERE id = ?;
-- name: UpdateTestAttempt :execrows
UPDATE test_attempts SET target_identity=?, phase=?, outcome=?, cleanup_state=?, recording_gap=?, cancelled_at=?, finished_at=? WHERE id=?;
-- name: BindTestTools :exec
INSERT INTO session_test_tools(session_id, attempt_id, profile_id) VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET attempt_id=excluded.attempt_id, profile_id=excluded.profile_id;
-- name: GetTestToolBinding :one
SELECT * FROM session_test_tools WHERE session_id = ?;
-- name: CreateTestWorkerLegs :exec
INSERT INTO test_worker_legs(session_id, base_run_id, head_run_id, timeout_seconds) VALUES (?, ?, ?, ?);
-- name: GetTestWorkerLegs :one
SELECT * FROM test_worker_legs WHERE session_id = ?;
