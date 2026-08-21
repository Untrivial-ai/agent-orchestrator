-- name: GetAgentInventoryCache :one
SELECT inventory_json, observed_at
FROM agent_inventory_cache
WHERE id = 1;

-- name: UpsertAgentInventoryCache :exec
INSERT INTO agent_inventory_cache (id, inventory_json, observed_at)
VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    inventory_json = excluded.inventory_json,
    observed_at = excluded.observed_at;
