-- name: UpsertAgentInstallJob :exec
INSERT INTO agent_install_jobs (
    target, status, method, command, expected_destination, output, error,
    started_at, finished_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(target) DO UPDATE SET
    status = excluded.status,
    method = excluded.method,
    command = excluded.command,
    expected_destination = excluded.expected_destination,
    output = excluded.output,
    error = excluded.error,
    started_at = excluded.started_at,
    finished_at = excluded.finished_at,
    updated_at = excluded.updated_at;

-- name: GetAgentInstallJob :one
SELECT target, status, method, command, expected_destination, output, error,
       started_at, finished_at, updated_at
FROM agent_install_jobs
WHERE target = ?;

-- name: ListAgentInstallJobs :many
SELECT target, status, method, command, expected_destination, output, error,
       started_at, finished_at, updated_at
FROM agent_install_jobs
ORDER BY target;

-- name: InterruptActiveAgentInstallJobs :exec
UPDATE agent_install_jobs
SET status = 'interrupted',
    error = CASE
        WHEN error = '' THEN 'AO restarted before this job completed.'
        ELSE error || '\nAO restarted before this job completed.'
    END,
    finished_at = ?,
    updated_at = ?
WHERE status IN ('installing', 'verifying');
