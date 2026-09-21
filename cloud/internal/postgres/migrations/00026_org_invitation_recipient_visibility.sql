-- +goose Up
DROP POLICY ao_org_invitations_tenant_policy ON ao_org_invitations;
CREATE POLICY ao_org_invitations_tenant_policy ON ao_org_invitations
    USING (
        org_id = ao_current_org_id()
        OR invited_user_id = ao_current_user_id()
    )
    WITH CHECK (org_id = ao_current_org_id());

-- +goose Down
DROP POLICY ao_org_invitations_tenant_policy ON ao_org_invitations;
CREATE POLICY ao_org_invitations_tenant_policy ON ao_org_invitations
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
