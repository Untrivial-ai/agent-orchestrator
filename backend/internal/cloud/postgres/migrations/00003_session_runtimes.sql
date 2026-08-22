-- +goose Up
CREATE TABLE ao_cloud_session_runtimes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES ao_cloud_workspaces(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL CHECK (btrim(session_id) <> ''),
    sandbox_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'provisioning'
        CHECK (state IN ('provisioning', 'running', 'stopped', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, session_id)
);
CREATE INDEX ao_cloud_session_runtimes_workspace_created_idx
    ON ao_cloud_session_runtimes(workspace_id, created_at DESC);

ALTER TABLE ao_cloud_session_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_cloud_session_runtimes FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_cloud_session_runtimes_all ON ao_cloud_session_runtimes
    FOR ALL
    USING (
        org_id = ao_current_org_id()
        AND ao_is_org_member(org_id, ao_current_user_id())
    )
    WITH CHECK (
        org_id = ao_current_org_id()
        AND ao_is_org_member(org_id, ao_current_user_id())
    );

REVOKE ALL ON TABLE ao_cloud_session_runtimes FROM PUBLIC;

-- +goose Down
DROP POLICY IF EXISTS ao_cloud_session_runtimes_all ON ao_cloud_session_runtimes;
DROP TABLE IF EXISTS ao_cloud_session_runtimes;
