-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN IF NOT EXISTS auto_inject_ci BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE ao_github_pr_applications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL REFERENCES ao_pull_requests(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pull_request_id, application_key)
);

CREATE TABLE ao_ci_feedback_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    application_key TEXT NOT NULL UNIQUE,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES ao_sessions(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL REFERENCES ao_pull_requests(id) ON DELETE CASCADE,
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'retry', 'delivered', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ao_ci_feedback_outbox_ready_idx
    ON ao_ci_feedback_outbox(status, next_attempt_at, created_at)
    WHERE status IN ('pending', 'retry');

-- Both tables are org-scoped tenant tables, so they carry forced row-level
-- security like every other tenant table. A tenant policy covers the withOrg
-- access paths (application dedup + outbox enqueue) and a service policy covers
-- the withService background paths (CI-feedback outbox lease/claim/finish).
ALTER TABLE ao_github_pr_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_github_pr_applications FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_github_pr_applications_tenant_policy ON ao_github_pr_applications
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_github_pr_applications_service_policy ON ao_github_pr_applications
    USING (ao_service_context()) WITH CHECK (ao_service_context());

ALTER TABLE ao_ci_feedback_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_ci_feedback_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_ci_feedback_outbox_tenant_policy ON ao_ci_feedback_outbox
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_ci_feedback_outbox_service_policy ON ao_ci_feedback_outbox
    USING (ao_service_context()) WITH CHECK (ao_service_context());

-- +goose Down
DROP INDEX IF EXISTS ao_ci_feedback_outbox_ready_idx;
DROP TABLE IF EXISTS ao_ci_feedback_outbox;
DROP TABLE IF EXISTS ao_github_pr_applications;
ALTER TABLE ao_sessions DROP COLUMN IF EXISTS auto_inject_ci;
