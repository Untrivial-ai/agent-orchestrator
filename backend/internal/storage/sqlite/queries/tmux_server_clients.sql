-- name: GetTmuxServerClient :one
SELECT socket_name, binary_path, binary_sha256, managed_retained, confirmed_at
FROM tmux_server_clients
WHERE socket_name = ?;

-- name: UpsertTmuxServerClient :exec
INSERT INTO tmux_server_clients (
    socket_name, binary_path, binary_sha256, managed_retained, confirmed_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(socket_name) DO UPDATE SET
    binary_path = excluded.binary_path,
    binary_sha256 = excluded.binary_sha256,
    managed_retained = excluded.managed_retained,
    confirmed_at = excluded.confirmed_at;

-- name: DeleteTmuxServerClient :exec
DELETE FROM tmux_server_clients WHERE socket_name = ?;
