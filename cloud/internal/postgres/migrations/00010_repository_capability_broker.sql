-- +goose Up

-- Production reserves the idempotency key before making the GitHub side
-- effect. The plaintext capability is encrypted only so an idempotent retry can
-- return the original value to the same server-side BFF operation.
CREATE TABLE ao_github_repository_capabilities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    github_user_id BIGINT NOT NULL CHECK (github_user_id > 0),
    target_environment TEXT NOT NULL
        CHECK (target_environment IN ('development', 'staging', 'production')),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    status TEXT NOT NULL DEFAULT 'creating'
        CHECK (status IN ('creating', 'active', 'revoked')),
    github_installation_id BIGINT NOT NULL CHECK (github_installation_id > 0),
    github_repository_id BIGINT,
    capability_hash BYTEA UNIQUE
        CHECK (capability_hash IS NULL OR octet_length(capability_hash) = 32),
    capability_ciphertext BYTEA,
    capability_nonce BYTEA
        CHECK (capability_nonce IS NULL OR octet_length(capability_nonce) = 12),
    repository_owner TEXT NOT NULL DEFAULT '',
    repository_name TEXT NOT NULL DEFAULT '',
    revoke_reason TEXT NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id, target_environment, idempotency_key),
    UNIQUE (org_id, id),
    UNIQUE (org_id, id, user_id, target_environment),
    CHECK (
        (status = 'creating'
            AND github_repository_id IS NULL
            AND capability_hash IS NULL
            AND capability_ciphertext IS NULL
            AND capability_nonce IS NULL)
        OR
        (status = 'active'
            AND github_repository_id IS NOT NULL
            AND capability_hash IS NOT NULL
            AND capability_ciphertext IS NOT NULL
            AND capability_nonce IS NOT NULL
            AND btrim(repository_owner) <> ''
            AND btrim(repository_name) <> ''
            AND revoked_at IS NULL)
        OR
        (status = 'revoked' AND revoked_at IS NOT NULL)
    )
);
CREATE INDEX ao_github_repository_capabilities_user_created_idx
    ON ao_github_repository_capabilities(user_id, created_at DESC);

-- A hash of 256 random capability bits is safe to use as an unprivileged
-- routing key. Keeping routing separate lets the broker establish the RLS
-- context without exposing the encrypted capability row.
CREATE TABLE ao_github_repository_capability_routes (
    capability_hash BYTEA PRIMARY KEY CHECK (octet_length(capability_hash) = 32),
    capability_id UUID NOT NULL,
    org_id UUID NOT NULL,
    user_id UUID NOT NULL,
    target_environment TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_github_repository_capability_routes_capability_fk
        FOREIGN KEY (
            org_id, capability_id, user_id, target_environment
        )
        REFERENCES ao_github_repository_capabilities(
            org_id, id, user_id, target_environment
        )
        ON DELETE CASCADE
);

ALTER TABLE ao_github_repository_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_github_repository_capabilities FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_github_repository_capabilities_tenant_policy
    ON ao_github_repository_capabilities
    USING (
        org_id = ao_current_org_id()
        AND user_id = ao_current_user_id()
    )
    WITH CHECK (
        org_id = ao_current_org_id()
        AND user_id = ao_current_user_id()
    );

-- Environment-owned projects keep the only recoverable copy outside
-- production, encrypted with that environment's provider credential key.
ALTER TABLE ao_projects
    ADD COLUMN github_installation_id BIGINT,
    ADD COLUMN github_capability_ciphertext BYTEA,
    ADD COLUMN github_capability_nonce BYTEA,
    ADD COLUMN github_authority_user_id TEXT,
    ADD COLUMN github_authority_environment TEXT;

ALTER TABLE ao_projects DROP CONSTRAINT ao_projects_check;
ALTER TABLE ao_projects ADD CONSTRAINT ao_projects_github_authority_check CHECK (
    (
        github_repository_id IS NULL
        AND github_repository_grant_id IS NULL
        AND github_installation_id IS NULL
        AND github_capability_ciphertext IS NULL
        AND github_capability_nonce IS NULL
        AND github_authority_user_id IS NULL
        AND github_authority_environment IS NULL
    )
    OR
    (
        github_repository_id IS NOT NULL
        AND github_repository_grant_id IS NOT NULL
        AND github_installation_id IS NULL
        AND github_capability_ciphertext IS NULL
        AND github_capability_nonce IS NULL
        AND github_authority_user_id IS NULL
        AND github_authority_environment IS NULL
    )
    OR
    (
        github_repository_id IS NOT NULL
        AND github_repository_grant_id IS NULL
        AND github_installation_id IS NOT NULL
        AND github_capability_ciphertext IS NOT NULL
        AND octet_length(github_capability_ciphertext) > 0
        AND github_capability_nonce IS NOT NULL
        AND octet_length(github_capability_nonce) = 12
        AND btrim(github_authority_user_id) <> ''
        AND github_authority_environment IN ('development', 'staging', 'production')
    )
);

-- +goose Down

ALTER TABLE ao_projects DROP CONSTRAINT IF EXISTS ao_projects_github_authority_check;
ALTER TABLE ao_projects
    DROP COLUMN IF EXISTS github_authority_environment,
    DROP COLUMN IF EXISTS github_authority_user_id,
    DROP COLUMN IF EXISTS github_capability_nonce,
    DROP COLUMN IF EXISTS github_capability_ciphertext,
    DROP COLUMN IF EXISTS github_installation_id;
ALTER TABLE ao_projects ADD CONSTRAINT ao_projects_check CHECK (
    (github_repository_id IS NULL AND github_repository_grant_id IS NULL)
    OR (github_repository_id IS NOT NULL AND github_repository_grant_id IS NOT NULL)
);
DROP TABLE IF EXISTS ao_github_repository_capability_routes;
DROP POLICY IF EXISTS ao_github_repository_capabilities_tenant_policy
    ON ao_github_repository_capabilities;
DROP TABLE IF EXISTS ao_github_repository_capabilities;
