-- +goose Up
-- +goose StatementBegin
CREATE TABLE providers (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    api_protocol TEXT NOT NULL CHECK (api_protocol IN ('anthropic-compatible', 'openai-compatible')),
    base_url TEXT NOT NULL,
    secret_ref TEXT NOT NULL UNIQUE,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE provider_models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    display_name TEXT NOT NULL,
    model_name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(provider_id, model_name)
);

CREATE INDEX idx_provider_models_provider_order
    ON provider_models(provider_id, sort_order, display_name);

-- ciphertext is a Windows DPAPI blob. Plaintext credentials are never stored.
CREATE TABLE provider_secrets (
    secret_ref TEXT PRIMARY KEY REFERENCES providers(secret_ref) ON DELETE CASCADE,
    ciphertext BLOB NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE provider_audits (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    action TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL
);

CREATE INDEX idx_provider_audits_provider_created
    ON provider_audits(provider_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE provider_audits;
DROP TABLE provider_secrets;
DROP TABLE provider_models;
DROP TABLE providers;
-- +goose StatementEnd
