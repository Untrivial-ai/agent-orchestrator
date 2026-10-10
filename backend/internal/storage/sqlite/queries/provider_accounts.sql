-- name: LoadProviderAccounts :one
SELECT facts FROM provider_account_state WHERE id = 1;
-- name: SaveProviderAccounts :exec
UPDATE provider_account_state SET facts = ? WHERE id = 1;
