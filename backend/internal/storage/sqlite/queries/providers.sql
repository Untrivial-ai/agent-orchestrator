-- name: ListProviders :many
SELECT p.id, p.display_name, p.api_protocol, p.base_url, p.secret_ref, p.enabled,
       p.created_at, p.updated_at,
       EXISTS(SELECT 1 FROM provider_secrets s WHERE s.secret_ref = p.secret_ref) AS secret_configured
FROM providers p ORDER BY p.display_name, p.id;

-- name: GetProvider :one
SELECT p.id, p.display_name, p.api_protocol, p.base_url, p.secret_ref, p.enabled,
       p.created_at, p.updated_at,
       EXISTS(SELECT 1 FROM provider_secrets s WHERE s.secret_ref = p.secret_ref) AS secret_configured
FROM providers p WHERE p.id = ? LIMIT 1;

-- name: PutProvider :exec
INSERT INTO providers (id,display_name,api_protocol,base_url,secret_ref,enabled,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name, api_protocol=excluded.api_protocol,
base_url=excluded.base_url, enabled=excluded.enabled, updated_at=excluded.updated_at;

-- name: PutProviderSecret :exec
INSERT INTO provider_secrets(secret_ref,ciphertext,updated_at) VALUES(?,?,?)
ON CONFLICT(secret_ref) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at;

-- name: GetProviderSecret :one
SELECT ciphertext FROM provider_secrets WHERE secret_ref=? LIMIT 1;

-- name: DeleteProviderSecret :exec
DELETE FROM provider_secrets WHERE secret_ref=?;

-- name: ListProviderModels :many
SELECT id,provider_id,display_name,model_name,enabled,sort_order,created_at,updated_at
FROM provider_models WHERE provider_id=? ORDER BY sort_order,display_name,id;

-- name: GetProviderModel :one
SELECT id,provider_id,display_name,model_name,enabled,sort_order,created_at,updated_at
FROM provider_models WHERE id=? LIMIT 1;

-- name: PutProviderModel :exec
INSERT INTO provider_models(id,provider_id,display_name,model_name,enabled,sort_order,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,model_name=excluded.model_name,
enabled=excluded.enabled,sort_order=excluded.sort_order,updated_at=excluded.updated_at;

-- name: CreateProviderAudit :exec
INSERT INTO provider_audits(id,provider_id,action,detail,created_at) VALUES(?,?,?,?,?);
