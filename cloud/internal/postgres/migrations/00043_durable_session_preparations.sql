-- +goose Up

ALTER TABLE ao_sessions
    ADD COLUMN preparation_compatibility_version SMALLINT,
    ADD COLUMN preparation_compatibility_hash BYTEA,
    ADD CONSTRAINT ao_sessions_preparation_compatibility_check CHECK (
        (preparation_compatibility_version IS NULL AND preparation_compatibility_hash IS NULL)
        OR (
            preparation_compatibility_version > 0
            AND preparation_compatibility_hash IS NOT NULL
            AND octet_length(preparation_compatibility_hash) = 32
        )
    );

CREATE UNIQUE INDEX ao_sessions_active_preparation_compatibility_idx
    ON ao_sessions(org_id, created_by_user_id, preparation_compatibility_hash)
    WHERE is_preparation
      AND is_terminated = false
      AND preparation_compatibility_hash IS NOT NULL;

ALTER TABLE ao_sandboxes
    ADD COLUMN preparation_generation BIGINT NOT NULL DEFAULT 0
        CHECK (preparation_generation >= 0);

CREATE TABLE ao_preparation_attachments (
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    client_instance_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    attached_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    detached_at TIMESTAMPTZ,
    PRIMARY KEY (session_id, client_instance_id),
    CONSTRAINT ao_preparation_attachments_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE
);

CREATE INDEX ao_preparation_attachments_org_session_idx
    ON ao_preparation_attachments(org_id, session_id);

ALTER TABLE ao_preparation_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_preparation_attachments FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_preparation_attachments_tenant_policy ON ao_preparation_attachments
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());

-- +goose Down

DROP TABLE IF EXISTS ao_preparation_attachments;

ALTER TABLE ao_sandboxes
    DROP COLUMN IF EXISTS preparation_generation;

DROP INDEX IF EXISTS ao_sessions_active_preparation_compatibility_idx;
ALTER TABLE ao_sessions
    DROP CONSTRAINT IF EXISTS ao_sessions_preparation_compatibility_check,
    DROP COLUMN IF EXISTS preparation_compatibility_hash,
    DROP COLUMN IF EXISTS preparation_compatibility_version;
