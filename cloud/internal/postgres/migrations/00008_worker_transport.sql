-- +goose Up

CREATE TABLE ao_worker_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    worker_epoch BIGINT NOT NULL CHECK (worker_epoch > 0),
    kind TEXT NOT NULL CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.close'
    )),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'succeeded', 'failed', 'cancelled')),
    response JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(response) = 'object'),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_until TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (org_id, session_id, id),
    CONSTRAINT ao_worker_requests_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);
CREATE INDEX ao_worker_requests_claim_idx
    ON ao_worker_requests(org_id, session_id, worker_epoch, created_at)
    WHERE status IN ('pending', 'claimed');

CREATE TABLE ao_terminal_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    worker_epoch BIGINT NOT NULL CHECK (worker_epoch > 0),
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'workspace')),
    state TEXT NOT NULL DEFAULT 'opening'
        CHECK (state IN ('opening', 'open', 'closed', 'failed')),
    next_output_sequence BIGINT NOT NULL DEFAULT 1 CHECK (next_output_sequence > 0),
    output_bytes BIGINT NOT NULL DEFAULT 0 CHECK (output_bytes >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    UNIQUE (org_id, session_id, id),
    CONSTRAINT ao_terminal_sessions_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);
CREATE INDEX ao_terminal_sessions_active_idx
    ON ao_terminal_sessions(org_id, session_id, worker_epoch)
    WHERE state IN ('opening', 'open');

CREATE TABLE ao_terminal_output (
    terminal_id UUID NOT NULL,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    data BYTEA NOT NULL CHECK (octet_length(data) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (terminal_id, sequence),
    CONSTRAINT ao_terminal_output_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_terminal_output_terminal_fk
        FOREIGN KEY (org_id, session_id, terminal_id)
        REFERENCES ao_terminal_sessions(org_id, session_id, id)
        ON DELETE CASCADE
);
CREATE INDEX ao_terminal_output_replay_idx
    ON ao_terminal_output(org_id, session_id, terminal_id, sequence);

ALTER TABLE ao_worker_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_worker_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_worker_requests_tenant_policy ON ao_worker_requests
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());

ALTER TABLE ao_terminal_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_terminal_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_terminal_sessions_tenant_policy ON ao_terminal_sessions
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_terminal_sessions_service_policy ON ao_terminal_sessions
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

ALTER TABLE ao_terminal_output ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_terminal_output FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_terminal_output_tenant_policy ON ao_terminal_output
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());

-- Terminal tickets are redeemed by hash before the owning organization is
-- known, just like worker bootstrap tickets.
-- The existing service policy on ao_access_tickets provides this lookup.

-- +goose Down
DROP TABLE IF EXISTS ao_terminal_output;
DROP TABLE IF EXISTS ao_terminal_sessions;
DROP TABLE IF EXISTS ao_worker_requests;
