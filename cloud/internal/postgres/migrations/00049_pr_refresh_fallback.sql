-- +goose Up
CREATE TABLE ao_pr_refresh_fallbacks (
    pull_request_id UUID PRIMARY KEY,
    org_id UUID NOT NULL,
    due_at TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT ''
        CHECK (reason IN ('', 'webhook_failed', 'webhook_silent')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_pr_refresh_fallbacks_pr_fk
        FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id) ON DELETE CASCADE
);

CREATE INDEX ao_pr_refresh_fallbacks_due_idx
    ON ao_pr_refresh_fallbacks(due_at, updated_at)
    WHERE due_at IS NOT NULL;

ALTER TABLE ao_pr_refresh_fallbacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_pr_refresh_fallbacks FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_pr_refresh_fallbacks_tenant_policy ON ao_pr_refresh_fallbacks
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_pr_refresh_fallbacks_service_policy ON ao_pr_refresh_fallbacks
    USING (ao_service_context()) WITH CHECK (ao_service_context());

INSERT INTO ao_pr_refresh_fallbacks (pull_request_id, org_id, due_at, reason)
SELECT id, org_id, observed_at + interval '2 minutes', 'webhook_silent'
FROM ao_pull_requests
WHERE state = 'open';

-- +goose Down
DROP POLICY IF EXISTS ao_pr_refresh_fallbacks_service_policy ON ao_pr_refresh_fallbacks;
DROP POLICY IF EXISTS ao_pr_refresh_fallbacks_tenant_policy ON ao_pr_refresh_fallbacks;
DROP INDEX IF EXISTS ao_pr_refresh_fallbacks_due_idx;
DROP TABLE IF EXISTS ao_pr_refresh_fallbacks;
