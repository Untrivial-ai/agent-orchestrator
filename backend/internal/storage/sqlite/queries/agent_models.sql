-- name: GetAgentModelCatalog :one
SELECT agent_id, project_id, binary_version, catalog_json, source, fetched_at,
       metadata_json, input_fingerprint, last_success_at, refresh_state,
       refresh_error, retry_count, retry_at, generation
FROM agent_model_catalog
WHERE agent_id = ? AND project_id = ?;

-- name: ListAgentModelCatalogsByAgent :many
SELECT agent_id, project_id, binary_version, catalog_json, source, fetched_at,
       metadata_json, input_fingerprint, last_success_at, refresh_state,
       refresh_error, retry_count, retry_at, generation
FROM agent_model_catalog
WHERE agent_id = ?
ORDER BY project_id;

-- name: UpsertAgentModelCatalog :exec
INSERT INTO agent_model_catalog (
    agent_id, project_id, binary_version, catalog_json, source, fetched_at,
    metadata_json, input_fingerprint, last_success_at, refresh_state,
    refresh_error, retry_count, retry_at, generation
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id, project_id) DO UPDATE SET
    binary_version = excluded.binary_version,
    catalog_json = excluded.catalog_json,
    source = excluded.source,
    fetched_at = excluded.fetched_at,
    metadata_json = excluded.metadata_json,
    input_fingerprint = excluded.input_fingerprint,
    last_success_at = excluded.last_success_at,
    refresh_state = excluded.refresh_state,
    refresh_error = excluded.refresh_error,
    retry_count = excluded.retry_count,
    retry_at = excluded.retry_at,
    generation = excluded.generation
WHERE excluded.generation >= agent_model_catalog.generation;

-- name: ListAgentModelCatalogs :many
SELECT agent_id, project_id, binary_version, catalog_json, source, fetched_at,
       metadata_json, input_fingerprint, last_success_at, refresh_state,
       refresh_error, retry_count, retry_at, generation
FROM agent_model_catalog
ORDER BY project_id, agent_id;
