-- Durable provider-native session registry and agent-switch saga.
--
-- The registry is operational resume state and deliberately remains separate
-- from usage telemetry. A single AO session may retain several conversations
-- for the same harness; only a concrete native identity is unique.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN latest_user_prompt TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN latest_assistant_update TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN native_transcript_path TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- Agent ownership changes are session read-model invalidations just like
-- activity and lifecycle changes. Preserve the 0078 payload and guards while
-- adding the ownership facts introduced/used by agent switching.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS sessions_cdc_update;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
    OR OLD.harness <> NEW.harness
    OR OLD.runtime_launch_id <> NEW.runtime_launch_id
    OR OLD.agent_session_id <> NEW.agent_session_id
    OR OLD.native_transcript_path <> NEW.native_transcript_path
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode
        ),
        NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE agent_native_sessions (
    id                 TEXT PRIMARY KEY CHECK (length(id) > 0),
    ao_session_id      TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (length(harness) > 0),
    config_dir         TEXT NOT NULL DEFAULT '',
    native_session_id  TEXT NOT NULL DEFAULT '',
    transcript_path    TEXT NOT NULL DEFAULT '',
    last_generation_id TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMP NOT NULL,
    last_used_at       TIMESTAMP NOT NULL,
    CHECK (last_used_at >= created_at)
);
-- +goose StatementEnd

-- An empty native id represents a newly-created conversation whose provider
-- hook has not reported yet. Those rows must not collapse together. Once an id
-- is known, retries are idempotent on the complete provider/config identity.
-- +goose StatementBegin
CREATE UNIQUE INDEX idx_agent_native_sessions_native_identity
    ON agent_native_sessions (
        ao_session_id, harness, config_dir, native_session_id
    )
    WHERE native_session_id <> '';
-- +goose StatementEnd

-- Supports listing retained conversations newest-first. Resume availability is
-- always revalidated against live provider evidence before AO selects one.
-- +goose StatementBegin
CREATE INDEX idx_agent_native_sessions_resume_candidate
    ON agent_native_sessions (
        ao_session_id, last_used_at DESC, created_at DESC, id DESC
    );
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE agent_switches (
    id                         TEXT PRIMARY KEY CHECK (length(id) > 0),
    session_id                 TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    idempotency_key            TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    request_fingerprint        TEXT NOT NULL
        CHECK (
            length(request_fingerprint) = 67
            AND substr(request_fingerprint, 1, 3) = 'v1:'
            AND substr(request_fingerprint, 4) NOT GLOB '*[^0-9a-f]*'
        ),
    from_harness               TEXT NOT NULL CHECK (length(from_harness) > 0),
    target_harness             TEXT NOT NULL CHECK (length(target_harness) > 0),
    target_native_session_ref  TEXT REFERENCES agent_native_sessions (id) ON DELETE SET NULL,
    target_start_mode          TEXT NOT NULL DEFAULT ''
        CHECK (target_start_mode IN ('', 'fresh', 'resumed')),
    state                      TEXT NOT NULL DEFAULT 'preparing_handoff'
        CHECK (state IN (
            'preparing_handoff', 'stopping_source', 'source_stopped', 'starting_target',
            'target_ready', 'delivering_context', 'completed', 'failed'
        )),
    agent_handoff_status       TEXT NOT NULL DEFAULT 'not_attempted'
        CHECK (agent_handoff_status IN (
            'not_attempted', 'requested', 'received', 'unavailable',
            'timed_out', 'failed', 'rejected'
        )),
    agent_handoff_path         TEXT NOT NULL DEFAULT '',
    agent_handoff_hash         TEXT NOT NULL DEFAULT '',
    source_generation_id       TEXT NOT NULL CHECK (length(source_generation_id) > 0),
    target_generation_id       TEXT NOT NULL DEFAULT '',
    target_runtime_handle_id   TEXT NOT NULL DEFAULT '',
    target_acknowledged_at     TIMESTAMP,
    error_code                 TEXT NOT NULL DEFAULT '',
    requested_at               TIMESTAMP NOT NULL,
    updated_at                 TIMESTAMP NOT NULL,
    UNIQUE (session_id, idempotency_key),
    CHECK (from_harness <> target_harness),
    CHECK (
        (
            agent_handoff_status = 'received'
            AND agent_handoff_path <> ''
            AND length(agent_handoff_hash) = 64
            AND agent_handoff_hash NOT GLOB '*[^0-9a-f]*'
        )
        OR
        (agent_handoff_status <> 'received' AND agent_handoff_path = '' AND agent_handoff_hash = '')
    ),
    CHECK (state NOT IN ('completed', 'failed') OR agent_handoff_status <> 'requested'),
    CHECK (updated_at >= requested_at),
    CHECK (target_runtime_handle_id = '' OR target_generation_id <> ''),
    CHECK (target_acknowledged_at IS NULL OR target_generation_id <> ''),
    CHECK (target_acknowledged_at IS NULL OR target_acknowledged_at >= requested_at)
);
-- +goose StatementEnd

-- Foreign keys prove that a referenced native row exists; these triggers also
-- bind it to the switch's AO session and target harness. Keep the
-- invariant in SQLite so direct SQL and future store paths cannot bypass it.
-- +goose StatementBegin
CREATE TRIGGER agent_switches_target_native_scope_insert
BEFORE INSERT ON agent_switches
WHEN NEW.target_native_session_ref IS NOT NULL
    AND NOT EXISTS (
        SELECT 1 FROM agent_native_sessions
        WHERE id = NEW.target_native_session_ref
          AND ao_session_id = NEW.session_id
          AND harness = NEW.target_harness
    )
BEGIN
    SELECT RAISE(ABORT, 'agent switch target native session scope mismatch');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER agent_switches_target_native_scope_update
BEFORE UPDATE OF session_id, target_harness, target_native_session_ref ON agent_switches
WHEN NEW.target_native_session_ref IS NOT NULL
    AND NOT EXISTS (
        SELECT 1 FROM agent_native_sessions
        WHERE id = NEW.target_native_session_ref
          AND ao_session_id = NEW.session_id
          AND harness = NEW.target_harness
    )
BEGIN
    SELECT RAISE(ABORT, 'agent switch target native session scope mismatch');
END;
-- +goose StatementEnd

-- The durable constraint backs up the in-memory per-session switch lock and
-- prevents two daemon processes/recovery paths from starting competing owners.
-- +goose StatementBegin
CREATE UNIQUE INDEX idx_agent_switches_one_active_per_session
    ON agent_switches (session_id)
    WHERE state NOT IN ('completed', 'failed');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_agent_switches_session_history
    ON agent_switches (session_id, requested_at DESC, id DESC);
-- +goose StatementEnd

-- Switch progress is surfaced by refetching the owning session. The payload is
-- intentionally id-only, matching the other auxiliary-table CDC producers.
-- +goose StatementBegin
CREATE TRIGGER agent_switches_cdc_insert
AFTER INSERT ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'session_updated',
        json_object('id', NEW.session_id),
        NEW.updated_at
    );
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER agent_switches_cdc_update
AFTER UPDATE ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'session_updated',
        json_object('id', NEW.session_id),
        NEW.updated_at
    );
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_cdc_update;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_cdc_insert;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_target_native_scope_update;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_target_native_scope_insert;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS sessions_cdc_update;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE agent_switches;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE agent_native_sessions;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN native_transcript_path;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN latest_assistant_update;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN latest_user_prompt;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode
        ),
        NEW.updated_at);
END;
-- +goose StatementEnd
