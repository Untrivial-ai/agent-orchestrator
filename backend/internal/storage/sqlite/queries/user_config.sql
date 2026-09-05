-- name: GetUserConfig :one
SELECT config
FROM user_config
WHERE id = 1;

-- name: UpsertUserConfig :exec
INSERT INTO user_config (id, config) VALUES (1, ?)
ON CONFLICT (id) DO UPDATE SET config = excluded.config;
