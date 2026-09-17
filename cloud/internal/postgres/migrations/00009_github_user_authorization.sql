-- +goose Up

CREATE TABLE ao_github_user_auth_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    code_verifier_ciphertext BYTEA NOT NULL
        CHECK (octet_length(code_verifier_ciphertext) > 0),
    code_verifier_nonce BYTEA NOT NULL
        CHECK (octet_length(code_verifier_nonce) = 12),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, id),
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX ao_github_user_auth_attempts_user_created_idx
    ON ao_github_user_auth_attempts(user_id, created_at DESC);

-- Callback routing is deliberately separate from the RLS-protected secret row.
-- It contains only a hashed bearer state and lets an unauthenticated OAuth
-- callback establish the user-scoped transaction needed to read the attempt.
CREATE TABLE ao_github_user_auth_routes (
    state_hash BYTEA PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    attempt_id UUID NOT NULL,
    user_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_github_user_auth_routes_attempt_fk
        FOREIGN KEY (user_id, attempt_id)
        REFERENCES ao_github_user_auth_attempts(user_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_github_user_connections (
    user_id UUID PRIMARY KEY REFERENCES ao_users(id) ON DELETE CASCADE,
    github_user_id BIGINT NOT NULL UNIQUE CHECK (github_user_id > 0),
    github_login TEXT NOT NULL CHECK (btrim(github_login) <> ''),
    github_avatar_url TEXT NOT NULL DEFAULT '',
    access_token_ciphertext BYTEA NOT NULL
        CHECK (octet_length(access_token_ciphertext) > 0),
    access_token_nonce BYTEA NOT NULL CHECK (octet_length(access_token_nonce) = 12),
    access_token_expires_at TIMESTAMPTZ,
    refresh_token_ciphertext BYTEA,
    refresh_token_nonce BYTEA
        CHECK (refresh_token_nonce IS NULL OR octet_length(refresh_token_nonce) = 12),
    refresh_token_expires_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (refresh_token_ciphertext IS NULL AND refresh_token_nonce IS NULL
            AND refresh_token_expires_at IS NULL)
        OR
        (refresh_token_ciphertext IS NOT NULL AND refresh_token_nonce IS NOT NULL
            AND refresh_token_expires_at IS NOT NULL)
    )
);

-- Provider revocation callbacks know only the GitHub user ID. As with OAuth
-- state routing, this non-secret row establishes the user RLS context before
-- the encrypted connection is deleted.
CREATE TABLE ao_github_user_connection_routes (
    github_user_id BIGINT PRIMARY KEY CHECK (github_user_id > 0),
    user_id UUID NOT NULL UNIQUE
        REFERENCES ao_github_user_connections(user_id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE ao_github_user_auth_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_github_user_auth_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_github_user_auth_attempts_owner_policy
    ON ao_github_user_auth_attempts
    USING (user_id = ao_current_user_id())
    WITH CHECK (user_id = ao_current_user_id());

ALTER TABLE ao_github_user_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_github_user_connections FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_github_user_connections_owner_policy
    ON ao_github_user_connections
    USING (user_id = ao_current_user_id())
    WITH CHECK (user_id = ao_current_user_id());

-- +goose Down

DROP TABLE IF EXISTS ao_github_user_connection_routes;
DROP POLICY IF EXISTS ao_github_user_connections_owner_policy
    ON ao_github_user_connections;
DROP TABLE IF EXISTS ao_github_user_connections;
DROP TABLE IF EXISTS ao_github_user_auth_routes;
DROP POLICY IF EXISTS ao_github_user_auth_attempts_owner_policy
    ON ao_github_user_auth_attempts;
DROP TABLE IF EXISTS ao_github_user_auth_attempts;
