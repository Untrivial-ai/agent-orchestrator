-- Daemon-owned user preferences. One row, seeded by migration 0042, so a read
-- never has to handle absence.

-- name: GetAppSettings :one
SELECT * FROM app_settings WHERE id = 1;

-- name: SetDefaultSessionMode :exec
UPDATE app_settings SET default_session_mode = ?, updated_at = ? WHERE id = 1;

-- name: SetCloudOffering :exec
UPDATE app_settings SET cloud_offering = ?, updated_at = ? WHERE id = 1;
