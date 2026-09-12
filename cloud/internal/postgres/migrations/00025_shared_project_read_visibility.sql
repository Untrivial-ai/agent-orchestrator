-- +goose Up

-- ListSharedProjects (the "shared with me" sidebar) aggregates grants across
-- every org the recipient was shared into, so it can only ever set
-- ao.user_id, never a single ao.org_id — there isn't one value that would
-- be correct for every row. Migration 00021 widened
-- ao_project_share_grants' policy for exactly that reason, but the grant
-- listing joins to ao_projects to read the project's display name, and
-- ao_projects' own tenant policy (org_id = ao_current_org_id() only) still
-- hides that row with ao.org_id unset. The join silently returns zero rows,
-- so a real, active share grant never shows up anywhere.
--
-- Split the single ALL policy into a write policy (unchanged: still requires
-- real org membership, via ao.org_id) and a read policy that additionally
-- allows a project row visible to whoever holds an active share grant on it,
-- regardless of which org (if any) is current.
DROP POLICY ao_projects_tenant_policy ON ao_projects;

CREATE POLICY ao_projects_tenant_write_policy ON ao_projects
    FOR INSERT
    WITH CHECK (org_id = ao_current_org_id());

CREATE POLICY ao_projects_tenant_update_policy ON ao_projects
    FOR UPDATE
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());

CREATE POLICY ao_projects_tenant_delete_policy ON ao_projects
    FOR DELETE
    USING (org_id = ao_current_org_id());

CREATE POLICY ao_projects_tenant_read_policy ON ao_projects
    FOR SELECT
    USING (
        org_id = ao_current_org_id()
        OR EXISTS (
            SELECT 1
            FROM ao_project_share_grants grant_row
            WHERE grant_row.org_id = ao_projects.org_id
              AND grant_row.project_id = ao_projects.id
              AND grant_row.user_id = ao_current_user_id()
              AND grant_row.status = 'active'
        )
    );

-- +goose Down
DROP POLICY ao_projects_tenant_write_policy ON ao_projects;
DROP POLICY ao_projects_tenant_update_policy ON ao_projects;
DROP POLICY ao_projects_tenant_delete_policy ON ao_projects;
DROP POLICY ao_projects_tenant_read_policy ON ao_projects;

CREATE POLICY ao_projects_tenant_policy ON ao_projects
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
