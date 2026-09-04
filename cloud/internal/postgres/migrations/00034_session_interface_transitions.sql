-- +goose Up

-- A session runs exactly one conversation controller at a time. `interface`
-- records which one is currently committed: the provider's native terminal TUI
-- or AO's durable event-projected headless Chat controller. It can change only
-- through the interface-transition coordinator below, never by a direct write.
ALTER TABLE ao_sessions
    ADD COLUMN interface TEXT NOT NULL DEFAULT 'tui'
        CHECK (interface IN ('tui', 'chat'));

-- The interface-transition coordinator runs in service context (no org scoping)
-- so a single replica can converge every org's handoffs. It needs the session's
-- harness to preflight the target controller; grant a read-only service policy
-- on ao_sessions for that purpose. Writes still require org/tenant context.
CREATE POLICY ao_sessions_interface_service_policy ON ao_sessions
    USING (ao_service_context());

-- One durable controller handoff. The session row remains the authority for the
-- currently committed interface; this row explains an in-progress gap where the
-- old controller has stopped and the new one is not ready yet. External process
-- work cannot share a Postgres transaction, so these phases make the operation
-- recoverable and visible to every client across control-plane replicas.
CREATE TABLE ao_interface_transitions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    source_interface TEXT NOT NULL CHECK (source_interface IN ('tui', 'chat')),
    target_interface TEXT NOT NULL CHECK (target_interface IN ('tui', 'chat')),
    policy TEXT NOT NULL CHECK (policy IN ('drain', 'interrupt')),
    phase TEXT NOT NULL DEFAULT 'requested' CHECK (phase IN (
        'requested', 'preflighting', 'draining', 'source_stopping',
        'source_stopped', 'target_starting', 'activating', 'completed',
        'failed', 'cancelled', 'recovery_required'
    )),
    native_conversation_id TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_detail TEXT NOT NULL DEFAULT '',
    notice_acknowledged_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    claimed_by TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMPTZ,
    UNIQUE (org_id, id),
    CONSTRAINT ao_interface_transitions_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE
);

-- One active transition per session. The coordinator relies on the partial
-- index to claim a session without racing another replica into a second writer.
CREATE UNIQUE INDEX ao_interface_transitions_one_active
    ON ao_interface_transitions(session_id)
    WHERE phase NOT IN (
        'completed', 'failed', 'cancelled', 'recovery_required'
    );
CREATE INDEX ao_interface_transitions_session_idx
    ON ao_interface_transitions(org_id, session_id, created_at DESC);
CREATE INDEX ao_interface_transitions_deliverable_idx
    ON ao_interface_transitions(org_id, completed_at)
    WHERE phase IN ('completed', 'failed', 'cancelled', 'recovery_required');
CREATE INDEX ao_interface_transitions_claim_idx
    ON ao_interface_transitions(claimed_at)
    WHERE claimed_by <> '';

-- Messages held while neither controller is allowed to accept work.
CREATE TABLE ao_interface_transition_messages (
    id BIGSERIAL PRIMARY KEY,
    transition_id UUID NOT NULL,
    client_message_id TEXT NOT NULL,
    message TEXT NOT NULL CHECK (btrim(message) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    UNIQUE (transition_id, client_message_id),
    CONSTRAINT ao_interface_transition_messages_transition_fk
        FOREIGN KEY (transition_id)
        REFERENCES ao_interface_transitions(id)
        ON DELETE CASCADE
);

ALTER TABLE ao_interface_transitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_interface_transitions FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_interface_transitions_tenant_policy ON ao_interface_transitions
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_interface_transitions_service_policy ON ao_interface_transitions
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- Messages are scoped through their parent transition; a tenant cannot reference
-- another organization's transition row, and service context may deliver them.
ALTER TABLE ao_interface_transition_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_interface_transition_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_interface_transition_messages_tenant_policy
    ON ao_interface_transition_messages
    USING (
        EXISTS (
            SELECT 1 FROM ao_interface_transitions t
            WHERE t.org_id = ao_current_org_id()
              AND t.id = transition_id
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM ao_interface_transitions t
            WHERE t.org_id = ao_current_org_id()
              AND t.id = transition_id
        )
    );
CREATE POLICY ao_interface_transition_messages_service_policy
    ON ao_interface_transition_messages
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- +goose Down

DROP TABLE IF EXISTS ao_interface_transition_messages;
DROP TABLE IF EXISTS ao_interface_transitions;
DROP POLICY IF EXISTS ao_sessions_interface_service_policy ON ao_sessions;
ALTER TABLE ao_sessions DROP COLUMN IF EXISTS interface;
