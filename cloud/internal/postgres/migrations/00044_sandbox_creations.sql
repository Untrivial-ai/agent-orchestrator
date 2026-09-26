-- +goose Up
CREATE TABLE ao_sandbox_creations (
    id UUID PRIMARY KEY,
    org_id UUID NOT NULL,
    session_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation >= 0),
    provider_environment_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'creating'
        CHECK (state IN ('creating', 'created', 'adopted', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, session_id) REFERENCES ao_sandboxes(org_id, session_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_sandbox_creations_pending_idx
    ON ao_sandbox_creations(session_id) WHERE state IN ('creating', 'created');
CREATE INDEX ao_sandbox_creations_org_session_idx ON ao_sandbox_creations(org_id, session_id);
ALTER TABLE ao_sandbox_creations ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_sandbox_creations FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_sandbox_creations_tenant_policy ON ao_sandbox_creations
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());

-- +goose Down
DROP TABLE ao_sandbox_creations;
