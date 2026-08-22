-- +goose Up
CREATE TABLE ao_cloud_workspaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    owner_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    repository_url TEXT NOT NULL CHECK (btrim(repository_url) <> ''),
    repository_ref TEXT NOT NULL DEFAULT '',
    sandbox_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'provisioning', 'ready', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ao_cloud_workspaces_org_created_idx
    ON ao_cloud_workspaces(org_id, created_at DESC);

ALTER TABLE ao_cloud_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_cloud_workspaces FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_cloud_workspaces_select ON ao_cloud_workspaces
    FOR SELECT
    USING (ao_is_org_member(org_id, ao_current_user_id()));
CREATE POLICY ao_cloud_workspaces_insert ON ao_cloud_workspaces
    FOR INSERT
    WITH CHECK (
        org_id = ao_current_org_id()
        AND owner_user_id = ao_current_user_id()
        AND ao_is_org_member(org_id, ao_current_user_id())
    );
CREATE POLICY ao_cloud_workspaces_update ON ao_cloud_workspaces
    FOR UPDATE
    USING (
        org_id = ao_current_org_id()
        AND owner_user_id = ao_current_user_id()
    )
    WITH CHECK (
        org_id = ao_current_org_id()
        AND owner_user_id = ao_current_user_id()
    );

REVOKE ALL ON TABLE ao_cloud_workspaces FROM PUBLIC;

-- +goose Down
DROP POLICY IF EXISTS ao_cloud_workspaces_update ON ao_cloud_workspaces;
DROP POLICY IF EXISTS ao_cloud_workspaces_insert ON ao_cloud_workspaces;
DROP POLICY IF EXISTS ao_cloud_workspaces_select ON ao_cloud_workspaces;
DROP TABLE IF EXISTS ao_cloud_workspaces;
