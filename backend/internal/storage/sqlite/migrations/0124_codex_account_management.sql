-- +goose Up
CREATE TABLE codex_active_account (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    account_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    activated_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE codex_account_switches (
    id TEXT PRIMARY KEY,
    source_account_id TEXT NOT NULL,
    target_account_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_fingerprint TEXT NOT NULL,
    expected_account_revision INTEGER NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN (
        'requested', 'stopping_sessions',
        'sessions_stopped', 'checkpointing_source', 'activating_target',
        'verifying_target', 'restarting_sessions', 'rollback_required',
        'recovery_required', 'completed', 'failed'
    )),
    failure_code TEXT NOT NULL DEFAULT '',
    credentials_committed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE UNIQUE INDEX idx_codex_account_switches_one_active
ON codex_account_switches((1))
WHERE phase NOT IN ('completed', 'failed');

CREATE TABLE codex_account_switch_sessions (
    switch_id TEXT NOT NULL REFERENCES codex_account_switches(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    native_session_id TEXT NOT NULL,
    interface_mode TEXT NOT NULL CHECK (interface_mode IN ('tui', 'chat')),
    source_handle_id TEXT NOT NULL DEFAULT '',
    source_generation TEXT NOT NULL DEFAULT '',
    was_running BOOLEAN NOT NULL,
    stop_state TEXT NOT NULL CHECK (stop_state IN ('pending', 'stopped', 'failed')),
    restart_state TEXT NOT NULL CHECK (restart_state IN ('pending', 'restarted', 'skipped', 'failed')),
    reviewer_was_running BOOLEAN NOT NULL DEFAULT FALSE,
    reviewer_source_handle_id TEXT NOT NULL DEFAULT '',
    reviewer_native_session_id TEXT NOT NULL DEFAULT '',
    reviewer_stop_state TEXT NOT NULL DEFAULT 'skipped' CHECK (reviewer_stop_state IN ('pending', 'stopped', 'skipped', 'failed')),
    reviewer_restart_state TEXT NOT NULL DEFAULT 'skipped' CHECK (reviewer_restart_state IN ('pending', 'restarted', 'skipped', 'failed')),
    error_code TEXT NOT NULL DEFAULT '',
    stopped_at TIMESTAMP,
    restarted_at TIMESTAMP,
    PRIMARY KEY (switch_id, session_id)
);

-- +goose StatementBegin
CREATE TRIGGER codex_account_switches_cdc_insert
AFTER INSERT ON codex_account_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ss.session_id, 'session_updated',
           json_object('id', ss.session_id, 'sessionId', ss.session_id), NEW.created_at
    FROM codex_account_switch_sessions ss
    JOIN sessions s ON s.id = ss.session_id
    WHERE ss.switch_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER codex_account_switches_cdc_update
AFTER UPDATE OF phase, failure_code ON codex_account_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ss.session_id, 'session_updated',
           json_object('id', ss.session_id, 'sessionId', ss.session_id), NEW.updated_at
    FROM codex_account_switch_sessions ss
    JOIN sessions s ON s.id = ss.session_id
    WHERE ss.switch_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER codex_account_switch_sessions_cdc
AFTER UPDATE ON codex_account_switch_sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id),
           COALESCE(NEW.restarted_at, NEW.stopped_at, CURRENT_TIMESTAMP)
    FROM sessions s WHERE s.id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER codex_account_switch_sessions_cdc_insert
AFTER INSERT ON codex_account_switch_sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id), CURRENT_TIMESTAMP
    FROM sessions s WHERE s.id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS codex_account_switch_sessions_cdc_insert;
DROP TRIGGER IF EXISTS codex_account_switch_sessions_cdc;
DROP TRIGGER IF EXISTS codex_account_switches_cdc_update;
DROP TRIGGER IF EXISTS codex_account_switches_cdc_insert;
DROP TABLE IF EXISTS codex_account_switch_sessions;
DROP INDEX IF EXISTS idx_codex_account_switches_one_active;
DROP TABLE IF EXISTS codex_account_switches;
DROP TABLE IF EXISTS codex_active_account;
