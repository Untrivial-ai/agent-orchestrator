-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE FUNCTION ao_current_org_id() RETURNS UUID
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('ao.org_id', true), '')::UUID
$$;

CREATE FUNCTION ao_current_user_id() RETURNS UUID
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('ao.user_id', true), '')::UUID
$$;

CREATE FUNCTION ao_current_external_org_id() RETURNS TEXT
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('ao.external_org_id', true), '')
$$;

CREATE TABLE ao_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auth_provider TEXT NOT NULL CHECK (auth_provider IN ('workos', 'local')),
    external_user_id TEXT NOT NULL,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    password_hash TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (auth_provider, external_user_id),
    CHECK (
        (auth_provider = 'local' AND password_hash IS NOT NULL)
        OR (auth_provider <> 'local' AND password_hash IS NULL)
    )
);
CREATE UNIQUE INDEX ao_users_local_email_key
    ON ao_users (lower(email))
    WHERE auth_provider = 'local';

CREATE TABLE ao_auth_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX ao_auth_sessions_user_idx ON ao_auth_sessions(user_id, expires_at);
CREATE INDEX ao_auth_sessions_expiry_idx ON ao_auth_sessions(expires_at);

CREATE TABLE ao_organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auth_provider TEXT NOT NULL CHECK (auth_provider IN ('workos', 'local')),
    external_org_id TEXT,
    slug TEXT NOT NULL UNIQUE CHECK (slug = lower(slug) AND slug ~ '^[a-z0-9][a-z0-9-]*$'),
    display_name TEXT NOT NULL CHECK (btrim(display_name) <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('personal', 'team', 'enterprise')),
    plan TEXT NOT NULL DEFAULT 'free',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    owner_user_id UUID REFERENCES ao_users(id) ON DELETE RESTRICT,
    created_by_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (auth_provider, external_org_id),
    CHECK (kind <> 'personal' OR owner_user_id IS NOT NULL)
);

CREATE TABLE ao_org_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    external_membership_id TEXT,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id),
    UNIQUE (org_id, id)
);
CREATE UNIQUE INDEX ao_org_memberships_external_key
    ON ao_org_memberships(org_id, external_membership_id)
    WHERE external_membership_id IS NOT NULL;
CREATE INDEX ao_org_memberships_user_idx ON ao_org_memberships(user_id, status);

CREATE TABLE ao_org_invitations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    invited_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL,
    invited_by_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE RESTRICT,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'declined', 'revoked', 'expired')),
    token_hash BYTEA UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '14 days'),
    accepted_at TIMESTAMPTZ,
    declined_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    CHECK (token_hash IS NULL OR octet_length(token_hash) = 32)
);
CREATE UNIQUE INDEX ao_org_invitations_pending_email_key
    ON ao_org_invitations(org_id, lower(email))
    WHERE status = 'pending';

CREATE TABLE ao_user_scm_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('github')),
    external_login TEXT NOT NULL,
    external_account_id BIGINT NOT NULL CHECK (external_account_id > 0),
    encrypted_access_token BYTEA NOT NULL,
    access_token_nonce BYTEA NOT NULL,
    encrypted_refresh_token BYTEA,
    refresh_token_nonce BYTEA,
    token_expires_at TIMESTAMPTZ,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired')),
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider),
    CHECK (
        (encrypted_refresh_token IS NULL AND refresh_token_nonce IS NULL)
        OR (encrypted_refresh_token IS NOT NULL AND refresh_token_nonce IS NOT NULL)
    )
);

CREATE TABLE ao_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (btrim(display_name) <> ''),
    repository_url TEXT NOT NULL CHECK (btrim(repository_url) <> ''),
    default_branch TEXT NOT NULL DEFAULT 'main' CHECK (btrim(default_branch) <> ''),
    github_repository_id BIGINT,
    github_repository_grant_id UUID,
    config JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, repository_url),
    CHECK (
        (github_repository_id IS NULL AND github_repository_grant_id IS NULL)
        OR (github_repository_id IS NOT NULL AND github_repository_grant_id IS NOT NULL)
    )
);
CREATE INDEX ao_projects_org_created_idx ON ao_projects(org_id, created_at DESC, id DESC);

CREATE TABLE ao_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('worker', 'orchestrator')),
    harness TEXT NOT NULL CHECK (btrim(harness) <> ''),
    display_name TEXT NOT NULL CHECK (btrim(display_name) <> ''),
    branch TEXT NOT NULL CHECK (btrim(branch) <> ''),
    prompt TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT 'standard' CHECK (mode IN ('read-only', 'standard', 'trusted')),
    denied_commands TEXT[] NOT NULL DEFAULT '{}',
    activity_state TEXT NOT NULL DEFAULT 'idle'
        CHECK (activity_state IN ('active', 'idle', 'waiting_input', 'blocked', 'exited')),
    is_terminated BOOLEAN NOT NULL DEFAULT false,
    agent_session_id TEXT NOT NULL DEFAULT '',
    next_sequence BIGINT NOT NULL DEFAULT 1 CHECK (next_sequence > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, project_id, id),
    CONSTRAINT ao_sessions_project_fk
        FOREIGN KEY (org_id, project_id)
        REFERENCES ao_projects(org_id, id)
        ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_sessions_one_active_orchestrator
    ON ao_sessions(project_id)
    WHERE kind = 'orchestrator' AND is_terminated = false;
CREATE INDEX ao_sessions_org_updated_idx ON ao_sessions(org_id, updated_at DESC, id DESC);
CREATE INDEX ao_sessions_org_project_updated_idx
    ON ao_sessions(org_id, project_id, updated_at DESC, id DESC);

CREATE TABLE ao_provider_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    label TEXT NOT NULL CHECK (btrim(label) <> ''),
    encrypted_secret BYTEA NOT NULL,
    secret_nonce BYTEA NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    validation_state TEXT NOT NULL DEFAULT 'pending'
        CHECK (validation_state IN ('pending', 'valid', 'invalid')),
    validated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, provider, label)
);

CREATE TABLE ao_sandboxes (
    session_id UUID PRIMARY KEY,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('ecs', 'daytona', 'docker')),
    provider_environment_id TEXT,
    provider_connection_id UUID,
    desired_state TEXT NOT NULL DEFAULT 'running' CHECK (desired_state IN ('running', 'stopped')),
    observed_state TEXT NOT NULL DEFAULT 'requested'
        CHECK (observed_state IN (
            'requested', 'provisioning', 'bootstrapping', 'ready', 'running',
            'stopping', 'stopped', 'disconnected', 'failed'
        )),
    resource_profile JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_profile) = 'object'),
    auto_stop_minutes INTEGER NOT NULL DEFAULT 30 CHECK (auto_stop_minutes > 0),
    worker_last_seen_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    reconcile_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    reconcile_lease_until TIMESTAMPTZ,
    reconcile_lease_owner TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, session_id),
    CONSTRAINT ao_sandboxes_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_sandboxes_provider_connection_fk
        FOREIGN KEY (org_id, provider_connection_id)
        REFERENCES ao_provider_connections(org_id, id)
        ON DELETE SET NULL (provider_connection_id)
);
CREATE UNIQUE INDEX ao_sandboxes_provider_environment_key
    ON ao_sandboxes(provider, provider_environment_id)
    WHERE provider_environment_id IS NOT NULL;
CREATE INDEX ao_sandboxes_reconcile_idx
    ON ao_sandboxes(reconcile_after, reconcile_lease_until)
    WHERE desired_state <> observed_state;

CREATE TABLE ao_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    type TEXT NOT NULL CHECK (btrim(type) <> ''),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, sequence),
    UNIQUE (org_id, session_id, sequence),
    CONSTRAINT ao_events_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE
);
CREATE INDEX ao_events_replay_idx ON ao_events(org_id, session_id, sequence);

CREATE TABLE ao_turns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    user_message_sequence BIGINT NOT NULL CHECK (user_message_sequence > 0),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'provisioning', 'running', 'cancel_requested', 'completed', 'failed')),
    worker_epoch BIGINT NOT NULL DEFAULT 0 CHECK (worker_epoch >= 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, user_message_sequence),
    CONSTRAINT ao_turns_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_turns_user_message_fk
        FOREIGN KEY (org_id, session_id, user_message_sequence)
        REFERENCES ao_events(org_id, session_id, sequence)
        ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_turns_one_active_per_session
    ON ao_turns(session_id)
    WHERE state IN ('queued', 'provisioning', 'running', 'cancel_requested');

CREATE TABLE ao_commands (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID,
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    kind TEXT NOT NULL CHECK (btrim(kind) <> ''),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'accepted'
        CHECK (status IN ('accepted', 'running', 'succeeded', 'failed')),
    result JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, idempotency_key),
    CONSTRAINT ao_commands_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_worker_connections (
    session_id UUID NOT NULL,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    sandbox_id UUID NOT NULL,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    worker_id TEXT NOT NULL CHECK (btrim(worker_id) <> ''),
    version TEXT NOT NULL,
    capabilities JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(capabilities) = 'array'),
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    disconnected_at TIMESTAMPTZ,
    ready_at TIMESTAMPTZ,
    PRIMARY KEY (session_id, epoch),
    CONSTRAINT ao_worker_connections_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_worker_connections_sandbox_fk
        FOREIGN KEY (org_id, sandbox_id)
        REFERENCES ao_sandboxes(org_id, session_id)
        ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_worker_connections_current_worker_idx
    ON ao_worker_connections(session_id)
    WHERE disconnected_at IS NULL;

CREATE TABLE ao_access_tickets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    purpose TEXT NOT NULL CHECK (btrim(purpose) <> ''),
    scopes TEXT[] NOT NULL DEFAULT '{}',
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    worker_epoch BIGINT,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_access_tickets_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

CREATE TABLE ao_audit_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    actor_user_id UUID REFERENCES ao_users(id) ON DELETE SET NULL,
    action TEXT NOT NULL CHECK (btrim(action) <> ''),
    resource_type TEXT NOT NULL CHECK (btrim(resource_type) <> ''),
    resource_id TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ao_audit_events_org_created_idx ON ao_audit_events(org_id, created_at DESC);
-- +goose StatementBegin
CREATE FUNCTION ao_reject_audit_mutation() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ao_audit_events is append-only';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER ao_audit_events_append_only
    BEFORE UPDATE OR DELETE ON ao_audit_events
    FOR EACH ROW EXECUTE FUNCTION ao_reject_audit_mutation();

CREATE TABLE ao_project_share_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    session_id UUID,
    created_by_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE RESTRICT,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    role TEXT NOT NULL CHECK (role IN ('viewer', 'editor')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    access_scope TEXT NOT NULL DEFAULT 'anyone' CHECK (access_scope IN ('anyone', 'restricted')),
    recipients JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(recipients) = 'array'),
    interaction TEXT NOT NULL DEFAULT 'view' CHECK (interaction IN ('view', 'interact')),
    mode_cap TEXT CHECK (mode_cap IN ('read-only', 'standard', 'trusted')),
    denied_commands TEXT[],
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, project_id, id),
    CONSTRAINT ao_project_share_links_project_fk
        FOREIGN KEY (org_id, project_id)
        REFERENCES ao_projects(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_project_share_links_session_fk
        FOREIGN KEY (org_id, project_id, session_id)
        REFERENCES ao_sessions(org_id, project_id, id)
        ON DELETE CASCADE,
    CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE TABLE ao_project_share_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_link_id UUID NOT NULL,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    session_id UUID,
    user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE CASCADE,
    shared_by_user_id UUID NOT NULL REFERENCES ao_users(id) ON DELETE RESTRICT,
    role TEXT NOT NULL CHECK (role IN ('viewer', 'editor')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    redeemed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, project_id, id),
    CONSTRAINT ao_project_share_grants_link_fk
        FOREIGN KEY (org_id, project_id, share_link_id)
        REFERENCES ao_project_share_links(org_id, project_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_project_share_grants_session_fk
        FOREIGN KEY (org_id, project_id, session_id)
        REFERENCES ao_sessions(org_id, project_id, id)
        ON DELETE CASCADE
);
CREATE UNIQUE INDEX ao_project_share_grants_active_idx
    ON ao_project_share_grants(user_id, org_id, project_id, COALESCE(session_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status = 'active';

CREATE TABLE ao_project_share_grant_sessions (
    grant_id UUID NOT NULL,
    session_id UUID NOT NULL,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('viewer', 'editor')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (grant_id, session_id),
    CONSTRAINT ao_project_share_grant_sessions_grant_fk
        FOREIGN KEY (org_id, project_id, grant_id)
        REFERENCES ao_project_share_grants(org_id, project_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_project_share_grant_sessions_session_fk
        FOREIGN KEY (org_id, project_id, session_id)
        REFERENCES ao_sessions(org_id, project_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_github_install_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    initiating_user_id UUID NOT NULL,
    state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    pending_github_installation_id BIGINT,
    pending_github_account_id BIGINT,
    pending_account_login TEXT,
    pending_account_type TEXT,
    pending_repository_selection TEXT CHECK (pending_repository_selection IN ('all', 'selected')),
    pending_repository_count INTEGER CHECK (pending_repository_count >= 0),
    pending_recorded_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_github_install_attempts_membership_fk
        FOREIGN KEY (org_id, initiating_user_id)
        REFERENCES ao_org_memberships(org_id, user_id)
        ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);

CREATE TABLE ao_github_installations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    github_installation_id BIGINT NOT NULL CHECK (github_installation_id > 0),
    github_account_id BIGINT NOT NULL CHECK (github_account_id > 0),
    account_login TEXT NOT NULL,
    account_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended', 'disconnected', 'deleted')),
    repository_selection TEXT NOT NULL CHECK (repository_selection IN ('all', 'selected')),
    permissions JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(permissions) = 'object'),
    events TEXT[] NOT NULL DEFAULT '{}',
    installed_by_user_id UUID NOT NULL,
    suspended_at TIMESTAMPTZ,
    disconnected_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (github_installation_id),
    CONSTRAINT ao_github_installations_membership_fk
        FOREIGN KEY (org_id, installed_by_user_id)
        REFERENCES ao_org_memberships(org_id, user_id)
        ON DELETE RESTRICT
);

CREATE TABLE ao_github_repositories (
    github_repository_id BIGINT PRIMARY KEY CHECK (github_repository_id > 0),
    github_owner_account_id BIGINT NOT NULL CHECK (github_owner_account_id > 0),
    name TEXT NOT NULL,
    full_name TEXT NOT NULL,
    html_url TEXT NOT NULL,
    clone_url TEXT NOT NULL,
    ssh_url TEXT NOT NULL DEFAULT '',
    default_branch TEXT NOT NULL DEFAULT 'main',
    visibility TEXT NOT NULL,
    is_private BOOLEAN NOT NULL DEFAULT false,
    is_archived BOOLEAN NOT NULL DEFAULT false,
    is_disabled BOOLEAN NOT NULL DEFAULT false,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    github_updated_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ao_github_repository_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    installation_id UUID NOT NULL,
    github_repository_id BIGINT NOT NULL
        REFERENCES ao_github_repositories(github_repository_id) ON DELETE RESTRICT,
    repository_selection TEXT NOT NULL CHECK (repository_selection IN ('all', 'selected')),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (org_id, github_repository_id, id),
    CONSTRAINT ao_github_repository_grants_installation_fk
        FOREIGN KEY (org_id, installation_id)
        REFERENCES ao_github_installations(org_id, id)
        ON DELETE RESTRICT
);
CREATE UNIQUE INDEX ao_github_repository_grants_active_idx
    ON ao_github_repository_grants(org_id, github_repository_id)
    WHERE revoked_at IS NULL;

ALTER TABLE ao_projects
    ADD CONSTRAINT ao_projects_github_repository_fk
        FOREIGN KEY (github_repository_id)
        REFERENCES ao_github_repositories(github_repository_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT ao_projects_github_grant_fk
        FOREIGN KEY (org_id, github_repository_id, github_repository_grant_id)
        REFERENCES ao_github_repository_grants(org_id, github_repository_id, id)
        ON DELETE RESTRICT;

CREATE TABLE ao_github_webhook_deliveries (
    github_delivery_id TEXT PRIMARY KEY,
    event TEXT NOT NULL,
    action TEXT NOT NULL DEFAULT '',
    github_installation_id BIGINT,
    github_repository_id BIGINT,
    payload BYTEA NOT NULL,
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'retry', 'processed', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processing_started_at TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ao_github_webhook_deliveries_ready_idx
    ON ao_github_webhook_deliveries(status, next_attempt_at, received_at)
    WHERE status IN ('pending', 'retry');

CREATE TABLE ao_issues (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    provider TEXT NOT NULL DEFAULT 'github' CHECK (provider IN ('github')),
    repository TEXT NOT NULL,
    number INTEGER NOT NULL CHECK (number > 0),
    url TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, provider, repository, number),
    CONSTRAINT ao_issues_project_fk
        FOREIGN KEY (org_id, project_id)
        REFERENCES ao_projects(org_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_session_issue_links (
    session_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, issue_id),
    CONSTRAINT ao_session_issue_links_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_session_issue_links_issue_fk
        FOREIGN KEY (org_id, issue_id)
        REFERENCES ao_issues(org_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_pull_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    provider TEXT NOT NULL DEFAULT 'github' CHECK (provider IN ('github')),
    repository TEXT NOT NULL,
    number INTEGER NOT NULL CHECK (number > 0),
    url TEXT NOT NULL,
    title TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('open', 'closed', 'merged')),
    draft BOOLEAN NOT NULL DEFAULT false,
    head_sha TEXT NOT NULL,
    source_branch TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    ci_state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (ci_state IN ('unknown', 'pending', 'passing', 'failing')),
    review_state TEXT NOT NULL DEFAULT 'none'
        CHECK (review_state IN ('none', 'approved', 'changes_requested', 'review_required')),
    mergeability TEXT NOT NULL DEFAULT 'unknown'
        CHECK (mergeability IN ('unknown', 'mergeable', 'conflicting', 'blocked', 'unstable')),
    checks JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(checks) = 'array'),
    claimed_by_session_id UUID,
    claimed_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    UNIQUE (org_id, provider, repository, number),
    CONSTRAINT ao_pull_requests_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ao_pull_requests_claimed_session_fk
        FOREIGN KEY (org_id, claimed_by_session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE SET NULL (claimed_by_session_id),
    CHECK (
        (claimed_by_session_id IS NULL AND claimed_at IS NULL)
        OR (claimed_by_session_id IS NOT NULL AND claimed_at IS NOT NULL)
    )
);

CREATE TABLE ao_pr_review_threads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL,
    provider_thread_id TEXT NOT NULL,
    is_resolved BOOLEAN NOT NULL DEFAULT false,
    is_outdated BOOLEAN NOT NULL DEFAULT false,
    path TEXT NOT NULL DEFAULT '',
    line INTEGER CHECK (line IS NULL OR line > 0),
    body TEXT NOT NULL DEFAULT '',
    author_login TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pull_request_id, provider_thread_id),
    CONSTRAINT ao_pr_review_threads_pr_fk
        FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id)
        ON DELETE CASCADE
);

-- +goose StatementBegin
DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'ao_org_invitations',
        'ao_projects',
        'ao_sessions',
        'ao_provider_connections',
        'ao_sandboxes',
        'ao_events',
        'ao_turns',
        'ao_commands',
        'ao_worker_connections',
        'ao_access_tickets',
        'ao_audit_events',
        'ao_project_share_links',
        'ao_project_share_grants',
        'ao_project_share_grant_sessions',
        'ao_github_install_attempts',
        'ao_github_installations',
        'ao_github_repository_grants',
        'ao_issues',
        'ao_session_issue_links',
        'ao_pull_requests',
        'ao_pr_review_threads'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id())',
            table_name || '_tenant_policy',
            table_name
        );
    END LOOP;

    ALTER TABLE ao_organizations ENABLE ROW LEVEL SECURITY;
    ALTER TABLE ao_organizations FORCE ROW LEVEL SECURITY;
    CREATE POLICY ao_organizations_tenant_policy ON ao_organizations
        USING (
            id = ao_current_org_id()
            OR (
                auth_provider = 'workos'
                AND external_org_id = ao_current_external_org_id()
            )
            OR EXISTS (
                SELECT 1
                FROM ao_org_memberships membership
                WHERE membership.org_id = ao_organizations.id
                  AND membership.user_id = ao_current_user_id()
                  AND membership.status = 'active'
            )
        )
        WITH CHECK (
            id = ao_current_org_id()
            OR (
                auth_provider = 'workos'
                AND external_org_id = ao_current_external_org_id()
            )
        );

    ALTER TABLE ao_org_memberships ENABLE ROW LEVEL SECURITY;
    ALTER TABLE ao_org_memberships FORCE ROW LEVEL SECURITY;
    CREATE POLICY ao_org_memberships_tenant_policy ON ao_org_memberships
        USING (
            org_id = ao_current_org_id()
            OR user_id = ao_current_user_id()
        )
        WITH CHECK (org_id = ao_current_org_id());

    ALTER TABLE ao_user_scm_identities ENABLE ROW LEVEL SECURITY;
    ALTER TABLE ao_user_scm_identities FORCE ROW LEVEL SECURITY;
    CREATE POLICY ao_user_scm_identities_owner_policy ON ao_user_scm_identities
        USING (
            org_id = ao_current_org_id()
            OR user_id = ao_current_user_id()
        )
        WITH CHECK (
            org_id = ao_current_org_id()
            AND user_id = ao_current_user_id()
        );
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP POLICY IF EXISTS ao_organizations_tenant_policy ON ao_organizations;
DROP TABLE IF EXISTS ao_pr_review_threads;
DROP TABLE IF EXISTS ao_pull_requests;
DROP TABLE IF EXISTS ao_session_issue_links;
DROP TABLE IF EXISTS ao_issues;
DROP TABLE IF EXISTS ao_github_webhook_deliveries;
ALTER TABLE IF EXISTS ao_projects
    DROP CONSTRAINT IF EXISTS ao_projects_github_grant_fk,
    DROP CONSTRAINT IF EXISTS ao_projects_github_repository_fk;
DROP TABLE IF EXISTS ao_github_repository_grants;
DROP TABLE IF EXISTS ao_github_repositories;
DROP TABLE IF EXISTS ao_github_installations;
DROP TABLE IF EXISTS ao_github_install_attempts;
DROP TABLE IF EXISTS ao_project_share_grant_sessions;
DROP TABLE IF EXISTS ao_project_share_grants;
DROP TABLE IF EXISTS ao_project_share_links;
DROP TABLE IF EXISTS ao_audit_events;
DROP FUNCTION IF EXISTS ao_reject_audit_mutation();
DROP TABLE IF EXISTS ao_access_tickets;
DROP TABLE IF EXISTS ao_worker_connections;
DROP TABLE IF EXISTS ao_commands;
DROP TABLE IF EXISTS ao_turns;
DROP TABLE IF EXISTS ao_events;
DROP TABLE IF EXISTS ao_sandboxes;
DROP TABLE IF EXISTS ao_provider_connections;
DROP TABLE IF EXISTS ao_sessions;
DROP TABLE IF EXISTS ao_projects;
DROP TABLE IF EXISTS ao_user_scm_identities;
DROP TABLE IF EXISTS ao_org_invitations;
DROP TABLE IF EXISTS ao_org_memberships;
DROP TABLE IF EXISTS ao_organizations;
DROP TABLE IF EXISTS ao_auth_sessions;
DROP TABLE IF EXISTS ao_users;
DROP FUNCTION IF EXISTS ao_current_external_org_id();
DROP FUNCTION IF EXISTS ao_current_user_id();
DROP FUNCTION IF EXISTS ao_current_org_id();
