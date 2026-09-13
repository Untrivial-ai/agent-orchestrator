-- name: UpsertDeviceSetupJob :exec
INSERT INTO device_setup_jobs (
    platform, state, stage, message, progress, downloaded_bytes, total_bytes,
    required_bytes, available_bytes, license_url, license_accepted, action_url,
    error_code, error, installed_version, started_at, finished_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(platform) DO UPDATE SET
    state = excluded.state,
    stage = excluded.stage,
    message = excluded.message,
    progress = excluded.progress,
    downloaded_bytes = excluded.downloaded_bytes,
    total_bytes = excluded.total_bytes,
    required_bytes = excluded.required_bytes,
    available_bytes = excluded.available_bytes,
    license_url = excluded.license_url,
    license_accepted = excluded.license_accepted,
    action_url = excluded.action_url,
    error_code = excluded.error_code,
    error = excluded.error,
    installed_version = excluded.installed_version,
    started_at = excluded.started_at,
    finished_at = excluded.finished_at,
    updated_at = excluded.updated_at;

-- name: GetDeviceSetupJob :one
SELECT platform, state, stage, message, progress, downloaded_bytes, total_bytes,
       required_bytes, available_bytes, license_url, license_accepted, action_url,
       error_code, error, installed_version, started_at, finished_at, updated_at
FROM device_setup_jobs
WHERE platform = ?;

-- name: InterruptActiveDeviceSetupJobs :exec
UPDATE device_setup_jobs
SET state = 'interrupted',
    error_code = 'SETUP_INTERRUPTED',
    error = 'AO restarted before setup completed. Retry to resume.',
    finished_at = ?,
    updated_at = ?
WHERE state IN ('queued', 'downloading', 'installing', 'creating', 'verifying');
