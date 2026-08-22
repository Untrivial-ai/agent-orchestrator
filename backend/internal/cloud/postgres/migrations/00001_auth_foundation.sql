-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE FUNCTION ao_current_user_id() RETURNS UUID
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('ao.user_id', true), '')::UUID
$$;

CREATE FUNCTION ao_current_org_id() RETURNS UUID
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('ao.org_id', true), '')::UUID
$$;

CREATE TABLE ao_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auth_provider TEXT NOT NULL CHECK (auth_provider = 'google'),
    external_user_id TEXT NOT NULL,
    email TEXT NOT NULL CHECK (btrim(email) <> ''),
    display_name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (auth_provider, external_user_id)
);

CREATE TABLE ao_auth_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX ao_auth_sessions_user_expiry_idx
    ON ao_auth_sessions(user_id, expires_at);
CREATE INDEX ao_auth_sessions_expiry_idx
    ON ao_auth_sessions(expires_at);

CREATE TABLE ao_organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE
        CHECK (slug = lower(slug) AND slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    display_name TEXT NOT NULL CHECK (btrim(display_name) <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('personal', 'team')),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled')),
    owner_user_id UUID REFERENCES ao_users(id) ON DELETE RESTRICT,
    created_by_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind <> 'personal' OR owner_user_id IS NOT NULL)
);

CREATE TABLE ao_org_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);
CREATE INDEX ao_org_memberships_user_status_idx
    ON ao_org_memberships(user_id, status);

-- SECURITY DEFINER avoids recursive membership-policy evaluation. The fixed
-- search path prevents object shadowing by a less-privileged role.
-- +goose StatementBegin
CREATE FUNCTION ao_is_org_member(candidate_org_id UUID, candidate_user_id UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM public.ao_org_memberships
        WHERE org_id = candidate_org_id
          AND user_id = candidate_user_id
          AND status = 'active'
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION ao_can_manage_org(candidate_org_id UUID, candidate_user_id UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM public.ao_org_memberships
        WHERE org_id = candidate_org_id
          AND user_id = candidate_user_id
          AND status = 'active'
          AND role IN ('owner', 'admin')
    )
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION ao_is_org_member(UUID, UUID) FROM PUBLIC;
REVOKE ALL ON FUNCTION ao_can_manage_org(UUID, UUID) FROM PUBLIC;

ALTER TABLE ao_organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_organizations FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_organizations_select ON ao_organizations
    FOR SELECT
    USING (ao_is_org_member(id, ao_current_user_id()));
CREATE POLICY ao_organizations_insert ON ao_organizations
    FOR INSERT
    WITH CHECK (
        id = ao_current_org_id()
        AND created_by_user_id = ao_current_user_id()
    );
CREATE POLICY ao_organizations_update ON ao_organizations
    FOR UPDATE
    USING (
        id = ao_current_org_id()
        AND ao_can_manage_org(id, ao_current_user_id())
    )
    WITH CHECK (id = ao_current_org_id());

ALTER TABLE ao_org_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_org_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_org_memberships_select ON ao_org_memberships
    FOR SELECT
    USING (
        user_id = ao_current_user_id()
        OR ao_is_org_member(org_id, ao_current_user_id())
    );
CREATE POLICY ao_org_memberships_insert ON ao_org_memberships
    FOR INSERT
    WITH CHECK (
        org_id = ao_current_org_id()
        AND (
            user_id = ao_current_user_id()
            OR ao_can_manage_org(org_id, ao_current_user_id())
        )
    );
CREATE POLICY ao_org_memberships_update ON ao_org_memberships
    FOR UPDATE
    USING (
        org_id = ao_current_org_id()
        AND ao_can_manage_org(org_id, ao_current_user_id())
    )
    WITH CHECK (org_id = ao_current_org_id());

REVOKE ALL ON TABLE ao_users, ao_auth_sessions, ao_organizations, ao_org_memberships FROM PUBLIC;

-- +goose Down
DROP POLICY IF EXISTS ao_org_memberships_update ON ao_org_memberships;
DROP POLICY IF EXISTS ao_org_memberships_insert ON ao_org_memberships;
DROP POLICY IF EXISTS ao_org_memberships_select ON ao_org_memberships;
DROP POLICY IF EXISTS ao_organizations_update ON ao_organizations;
DROP POLICY IF EXISTS ao_organizations_insert ON ao_organizations;
DROP POLICY IF EXISTS ao_organizations_select ON ao_organizations;
DROP FUNCTION IF EXISTS ao_can_manage_org(UUID, UUID);
DROP FUNCTION IF EXISTS ao_is_org_member(UUID, UUID);
DROP TABLE IF EXISTS ao_org_memberships;
DROP TABLE IF EXISTS ao_organizations;
DROP TABLE IF EXISTS ao_auth_sessions;
DROP TABLE IF EXISTS ao_users;
DROP FUNCTION IF EXISTS ao_current_org_id();
DROP FUNCTION IF EXISTS ao_current_user_id();
