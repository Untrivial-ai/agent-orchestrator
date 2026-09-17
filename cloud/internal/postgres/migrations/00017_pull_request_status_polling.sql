-- +goose Up

-- The pull-request status poller has no user principal and must scan open
-- pull requests across every organization, the same way the sandbox
-- reconciler and idle-pause scanner do for ao_sandboxes — see
-- ao_service_context() in migration 00005. Every write that follows a scan
-- still goes through withOrg, so row-level security confines it again.
CREATE POLICY ao_pull_requests_service_policy ON ao_pull_requests
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- +goose Down
DROP POLICY ao_pull_requests_service_policy ON ao_pull_requests;
