-- name: GetAgentModelCatalog :one
SELECT agent_id, project_id, binary_version, catalog_json, source, fetched_at
FROM agent_model_catalog
WHERE agent_id = ? AND project_id = ?;

-- name: ListAgentModelCatalogsByAgent :many
SELECT agent_id, project_id, binary_version, catalog_json, source, fetched_at
FROM agent_model_catalog
WHERE agent_id = ?
ORDER BY project_id;

-- name: UpsertAgentModelCatalog :exec
INSERT INTO agent_model_catalog (
    agent_id, project_id, binary_version, catalog_json, source, fetched_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id, project_id) DO UPDATE SET
    binary_version = excluded.binary_version,
    catalog_json = excluded.catalog_json,
    source = excluded.source,
    fetched_at = excluded.fetched_at;
