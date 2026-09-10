-- +goose Up

-- A share grant's recipient needs to list every project shared with them
-- across every org, including orgs they are not a member of — that's the
-- whole point of a share. The generic org-scoped tenant policy applied to
-- ao_project_share_grants by migration 00001 only ever exposes rows for
-- whichever single org is current, so it cannot answer that query. Widen it
-- the same way ao_org_memberships already is: a grant row is visible either
-- to the current org (the sharer's side) or to the user it was granted to
-- (the recipient's side), regardless of org.
DROP POLICY ao_project_share_grants_tenant_policy ON ao_project_share_grants;
CREATE POLICY ao_project_share_grants_tenant_policy ON ao_project_share_grants
    USING (org_id = ao_current_org_id() OR user_id = ao_current_user_id())
    WITH CHECK (org_id = ao_current_org_id());

-- +goose Down
DROP POLICY ao_project_share_grants_tenant_policy ON ao_project_share_grants;
CREATE POLICY ao_project_share_grants_tenant_policy ON ao_project_share_grants
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
