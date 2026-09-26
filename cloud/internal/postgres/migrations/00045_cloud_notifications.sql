-- +goose Up

CREATE TABLE ao_notification_ingress (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    session_id UUID NOT NULL,
    recipient_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL,
    worker_id TEXT NOT NULL CHECK (btrim(worker_id) <> ''),
    worker_epoch BIGINT NOT NULL CHECK (worker_epoch > 0),
    event_id TEXT NOT NULL CHECK (btrim(event_id) <> ''),
    event_type TEXT NOT NULL CHECK (event_type IN ('needs_input', 'agent_failed', 'agent_completed')),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'complete', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (worker_id, worker_epoch, event_id),
    CONSTRAINT ao_notification_ingress_project_fk
        FOREIGN KEY (org_id, project_id) REFERENCES ao_projects(org_id, id) ON DELETE CASCADE,
    CONSTRAINT ao_notification_ingress_session_fk
        FOREIGN KEY (org_id, project_id, session_id)
        REFERENCES ao_sessions(org_id, project_id, id) ON DELETE CASCADE
);
CREATE INDEX ao_notification_ingress_claim_idx
    ON ao_notification_ingress(next_attempt_at, created_at)
    WHERE status IN ('pending', 'claimed');

CREATE TABLE ao_notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    recipient_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    session_id UUID NOT NULL,
    pull_request_id UUID,
    source TEXT NOT NULL DEFAULT 'cloud' CHECK (source = 'cloud'),
    type TEXT NOT NULL CHECK (btrim(type) <> ''),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    body TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    dedupe_key TEXT NOT NULL CHECK (btrim(dedupe_key) <> ''),
    source_event_id TEXT NOT NULL CHECK (btrim(source_event_id) <> ''),
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'read')),
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    CONSTRAINT ao_notifications_project_fk
        FOREIGN KEY (org_id, project_id) REFERENCES ao_projects(org_id, id) ON DELETE CASCADE,
    CONSTRAINT ao_notifications_session_fk
        FOREIGN KEY (org_id, project_id, session_id)
        REFERENCES ao_sessions(org_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT ao_notifications_pull_request_fk
        FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_notifications_active_dedupe_idx
    ON ao_notifications(org_id, recipient_user_id, dedupe_key)
    WHERE resolved_at IS NULL;
CREATE INDEX ao_notifications_recipient_created_idx
    ON ao_notifications(org_id, recipient_user_id, created_at DESC, id DESC);

CREATE TABLE ao_notification_events (
    sequence BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    recipient_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    notification_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('notification_created', 'notification_updated', 'notification_resolved')),
    source_event_id TEXT NOT NULL CHECK (btrim(source_event_id) <> ''),
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_notification_events_notification_fk
        FOREIGN KEY (org_id, notification_id)
        REFERENCES ao_notifications(org_id, id) ON DELETE CASCADE
);
CREATE INDEX ao_notification_events_recipient_sequence_idx
    ON ao_notification_events(org_id, recipient_user_id, sequence);

ALTER TABLE ao_notification_ingress ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_notification_ingress FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_notification_ingress_tenant_policy ON ao_notification_ingress
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_notification_ingress_service_policy ON ao_notification_ingress
    USING (ao_service_context()) WITH CHECK (ao_service_context());

ALTER TABLE ao_notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_notifications_recipient_policy ON ao_notifications
    USING (org_id = ao_current_org_id() AND recipient_user_id = ao_current_user_id())
    WITH CHECK (org_id = ao_current_org_id() AND recipient_user_id = ao_current_user_id());
CREATE POLICY ao_notifications_service_policy ON ao_notifications
    USING (ao_service_context()) WITH CHECK (ao_service_context());

ALTER TABLE ao_notification_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_notification_events FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_notification_events_recipient_policy ON ao_notification_events
    USING (org_id = ao_current_org_id() AND recipient_user_id = ao_current_user_id())
    WITH CHECK (org_id = ao_current_org_id() AND recipient_user_id = ao_current_user_id());
CREATE POLICY ao_notification_events_service_policy ON ao_notification_events
    USING (ao_service_context()) WITH CHECK (ao_service_context());

-- +goose Down

DROP TABLE IF EXISTS ao_notification_events;
DROP TABLE IF EXISTS ao_notifications;
DROP TABLE IF EXISTS ao_notification_ingress;
