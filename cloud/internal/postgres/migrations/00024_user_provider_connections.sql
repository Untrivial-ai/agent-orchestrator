-- +goose Up

-- A session's creator, so a personal (user-level) credential fallback can
-- be resolved at worker-credential time without threading user identity
-- through the whole worker-auth call chain. Nullable: existing sessions
-- predate this column, and it is never required — the org-level connection
-- alone remains sufficient, this is purely an additional fallback source.
ALTER TABLE ao_sessions ADD COLUMN created_by_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL;

-- A coding-agent credential a user connects once and can use in every org
-- they belong to, mirroring ao_provider_connections' shape exactly but
-- keyed by user_id instead of org_id. Resolution (see WorkerAgentCredential)
-- always tries the org's own shared connection first; this is a fallback
-- for when the org has none, not an override of one that exists.
CREATE TABLE ao_user_provider_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    label TEXT NOT NULL CHECK (btrim(label) <> ''),
    encrypted_secret BYTEA NOT NULL,
    secret_nonce BYTEA NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    validation_state TEXT NOT NULL DEFAULT 'pending'
        CHECK (validation_state IN ('pending', 'valid', 'invalid')),
    validated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider, label)
);

ALTER TABLE ao_user_provider_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_user_provider_connections FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_user_provider_connections_owner_policy
    ON ao_user_provider_connections
    USING (user_id = ao_current_user_id())
    WITH CHECK (user_id = ao_current_user_id());

-- +goose Down
DROP TABLE IF EXISTS ao_user_provider_connections;
ALTER TABLE ao_sessions DROP COLUMN IF EXISTS created_by_user_id;
