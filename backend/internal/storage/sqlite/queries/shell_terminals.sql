-- name: InsertShellTerminal :one
INSERT INTO shell_terminals (
    handle_id, project_id, session_id, working_dir, title, app_run_id, created_at, transient
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SelectShellTerminalByHandleID :one
SELECT *
FROM shell_terminals
WHERE handle_id = ?;

-- name: SelectShellTerminalsByAppRunID :many
SELECT *
FROM shell_terminals
WHERE app_run_id = ?
ORDER BY created_at;

-- name: SelectShellTerminalsBySessionID :many
SELECT *
FROM shell_terminals
WHERE session_id = ?
ORDER BY created_at;

-- name: SelectShellTerminalsFromPreviousAppRuns :many
SELECT *
FROM shell_terminals
WHERE app_run_id <> ?
ORDER BY created_at;

-- name: UpdateShellTerminalTitle :one
UPDATE shell_terminals
SET title = ?
WHERE handle_id = ?
RETURNING *;

-- name: DeleteShellTerminalByHandleID :execrows
DELETE FROM shell_terminals
WHERE handle_id = ?;

-- name: DeleteShellTerminalsFromPreviousAppRuns :execrows
DELETE FROM shell_terminals
WHERE app_run_id <> ?;

-- name: SelectRestorableShellTerminals :many
SELECT * FROM shell_terminals
WHERE transient = FALSE OR app_run_id = ?
ORDER BY created_at;
