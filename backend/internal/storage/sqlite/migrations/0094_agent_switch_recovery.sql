-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- SQLite cannot widen CHECK constraints in place. Preserve the 0085 switch
-- shape while admitting the exact nonterminal markers used when source
-- shutdown or restoration cannot be confirmed automatically.
PRAGMA foreign_keys=OFF;

CREATE TABLE agent_switches_next (
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
    target_start_mode          TEXT NOT NULL DEFAULT '' CHECK (target_start_mode IN ('', 'fresh', 'resumed')),
    state                      TEXT NOT NULL DEFAULT 'preparing_handoff'
        CHECK (state IN ('preparing_handoff', 'stopping_source', 'source_stopped', 'starting_target', 'target_ready', 'delivering_context', 'completed', 'failed')),
    agent_handoff_status       TEXT NOT NULL DEFAULT 'not_attempted'
        CHECK (agent_handoff_status IN ('not_attempted', 'requested', 'received', 'unavailable', 'timed_out', 'failed', 'rejected')),
    source_transcript_status   TEXT NOT NULL DEFAULT 'not_attempted'
        CHECK (source_transcript_status IN ('not_attempted', 'available', 'unavailable')),
    semantic_handoff_included INTEGER NOT NULL DEFAULT 0 CHECK (semantic_handoff_included IN (0, 1)),
    agent_handoff_path         TEXT NOT NULL DEFAULT '',
    agent_handoff_hash         TEXT NOT NULL DEFAULT '',
    source_generation_id       TEXT NOT NULL CHECK (length(source_generation_id) > 0),
    target_generation_id       TEXT NOT NULL DEFAULT '',
    target_runtime_handle_id   TEXT NOT NULL DEFAULT '',
    target_acknowledged_at     TIMESTAMP,
    error_code                 TEXT NOT NULL DEFAULT ''
        CHECK (error_code IN (
            '', 'daemon_restart_pre_stop', 'daemon_restart_post_stop',
            'daemon_restart_unrecoverable_target', 'daemon_restart_before_delivery',
            'delivery_unconfirmed', 'source_session_terminated', 'source_stop_unconfirmed',
            'target_binary_missing', 'target_agent_unauthorized', 'target_start_unconfirmed',
            'source_restore_unconfirmed', 'request_cancelled', 'source_blocked',
            'failed_pre_stop', 'failed_post_stop', 'target_ready_failed', 'delivery_failed', 'switch_failed'
        )),
    requested_at               TIMESTAMP NOT NULL,
    updated_at                 TIMESTAMP NOT NULL,
    final_handoff_path         TEXT NOT NULL DEFAULT '',
    final_handoff_hash         TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, idempotency_key),
    CHECK (from_harness <> target_harness),
    CHECK (
        (agent_handoff_status = 'received' AND agent_handoff_path <> '' AND length(agent_handoff_hash) = 64 AND agent_handoff_hash NOT GLOB '*[^0-9a-f]*')
        OR (agent_handoff_status <> 'received' AND agent_handoff_path = '' AND agent_handoff_hash = '')
    ),
    CHECK (state NOT IN ('completed', 'failed') OR agent_handoff_status <> 'requested'),
    CHECK (
        (state = 'failed' AND error_code NOT IN ('', 'target_start_unconfirmed', 'source_restore_unconfirmed'))
        OR (state = 'starting_target' AND target_runtime_handle_id = '' AND error_code = 'target_start_unconfirmed')
        OR (state = 'stopping_source' AND error_code = 'source_stop_unconfirmed')
        OR (state IN ('source_stopped', 'starting_target') AND error_code = 'source_restore_unconfirmed')
        OR (state <> 'failed' AND error_code = '')
    ),
    CHECK (updated_at >= requested_at),
    CHECK (target_runtime_handle_id = '' OR target_generation_id <> ''),
    CHECK (target_acknowledged_at IS NULL OR target_generation_id <> ''),
    CHECK (target_acknowledged_at IS NULL OR target_acknowledged_at >= requested_at)
);

INSERT INTO agent_switches_next (
    id, session_id, idempotency_key, request_fingerprint, from_harness, target_harness,
    target_native_session_ref, target_start_mode, state, agent_handoff_status,
    source_transcript_status, semantic_handoff_included, agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id, target_acknowledged_at,
    error_code, requested_at, updated_at, final_handoff_path, final_handoff_hash
)
SELECT
    id, session_id, idempotency_key, request_fingerprint, from_harness, target_harness,
    target_native_session_ref, target_start_mode, state, agent_handoff_status,
    source_transcript_status, semantic_handoff_included, agent_handoff_path, agent_handoff_hash,
    source_generation_id, target_generation_id, target_runtime_handle_id, target_acknowledged_at,
    error_code, requested_at, updated_at, final_handoff_path, final_handoff_hash
FROM agent_switches;

DROP TABLE agent_switches;
ALTER TABLE agent_switches_next RENAME TO agent_switches;

CREATE UNIQUE INDEX idx_agent_switches_one_active_per_session
    ON agent_switches (session_id) WHERE state NOT IN ('completed', 'failed');
CREATE INDEX idx_agent_switches_session_history
    ON agent_switches (session_id, requested_at DESC, id DESC);

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

CREATE TRIGGER agent_switches_cdc_insert
AFTER INSERT ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

CREATE TRIGGER agent_switches_cdc_update
AFTER UPDATE ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rows may already carry the new recovery marker, so a safe downgrade cannot
-- restore the narrower 0085 CHECK constraint.
SELECT 1;
-- +goose StatementEnd
