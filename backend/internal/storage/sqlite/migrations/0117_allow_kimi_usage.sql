-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_sources_cdc_update;

CREATE TABLE usage_bindings_next (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (harness IN ('claude-code', 'codex', 'kimi')),
    native_root_id     TEXT NOT NULL CHECK (trim(native_root_id) <> ''),
    initial_model_id   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('discovering', 'active', 'finalizing', 'complete', 'partial')),
    last_error_code    TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMP NOT NULL,
    provider_hint      TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, harness, native_root_id)
);

CREATE TABLE usage_sources_next (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id          INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('claude_main', 'claude_subagent', 'codex_rollout', 'kimi_wire')),
    native_session_id   TEXT NOT NULL DEFAULT '',
    subagent_id         TEXT NOT NULL DEFAULT '',
    artifact_path       TEXT NOT NULL CHECK (trim(artifact_path) <> ''),
    file_identity       TEXT NOT NULL DEFAULT '',
    generation          INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    byte_offset         INTEGER NOT NULL DEFAULT 0 CHECK (byte_offset >= 0),
    parser_state_json   TEXT NOT NULL DEFAULT '{}',
    state               TEXT NOT NULL CHECK (state IN ('pending', 'active', 'complete', 'error')),
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    anomaly_count       INTEGER NOT NULL DEFAULT 0 CHECK (anomaly_count >= 0),
    next_retry_at       TIMESTAMP,
    last_error_code     TEXT NOT NULL DEFAULT '',
    updated_at          TIMESTAMP NOT NULL,
    UNIQUE (binding_id, artifact_path, generation)
);

INSERT INTO usage_bindings_next
SELECT id, session_id, harness, native_root_id, initial_model_id,
       state, last_error_code, updated_at, provider_hint
FROM usage_bindings;

INSERT INTO usage_sources_next
SELECT id, binding_id, kind, native_session_id, subagent_id, artifact_path,
       file_identity, generation, byte_offset, parser_state_json, state,
       failure_count, anomaly_count, next_retry_at, last_error_code, updated_at
FROM usage_sources;

DROP TABLE usage_sources;
DROP TABLE usage_bindings;
ALTER TABLE usage_bindings_next RENAME TO usage_bindings;
ALTER TABLE usage_sources_next RENAME TO usage_sources;

CREATE INDEX idx_usage_bindings_session_state ON usage_bindings (session_id, state);
CREATE INDEX idx_usage_sources_state_retry ON usage_sources (state, next_retry_at);
CREATE INDEX idx_usage_sources_binding_kind ON usage_sources (binding_id, kind);
CREATE INDEX idx_usage_sources_codex_native_latest
    ON usage_sources (kind, native_session_id, binding_id, generation DESC, id DESC);

CREATE TRIGGER usage_bindings_cdc_insert AFTER INSERT ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_update AFTER UPDATE ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_sources_cdc_update AFTER UPDATE ON usage_sources
WHEN OLD.anomaly_count IS NOT NEW.anomaly_count
  OR OLD.last_error_code IS NOT NEW.last_error_code
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ub.session_id, 'session_updated', json_object('id', ub.session_id), NEW.updated_at
    FROM usage_bindings ub JOIN sessions s ON s.id = ub.session_id WHERE ub.id = NEW.binding_id;
END;

COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_sources_cdc_update;

DELETE FROM model_usage_events
WHERE binding_id IN (SELECT id FROM usage_bindings WHERE harness = 'kimi');

CREATE TABLE usage_bindings_previous (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (harness IN ('claude-code', 'codex')),
    native_root_id     TEXT NOT NULL CHECK (trim(native_root_id) <> ''),
    initial_model_id   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('discovering', 'active', 'finalizing', 'complete', 'partial')),
    last_error_code    TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMP NOT NULL,
    provider_hint      TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, harness, native_root_id)
);

CREATE TABLE usage_sources_previous (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id          INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('claude_main', 'claude_subagent', 'codex_rollout')),
    native_session_id   TEXT NOT NULL DEFAULT '',
    subagent_id         TEXT NOT NULL DEFAULT '',
    artifact_path       TEXT NOT NULL CHECK (trim(artifact_path) <> ''),
    file_identity       TEXT NOT NULL DEFAULT '',
    generation          INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    byte_offset         INTEGER NOT NULL DEFAULT 0 CHECK (byte_offset >= 0),
    parser_state_json   TEXT NOT NULL DEFAULT '{}',
    state               TEXT NOT NULL CHECK (state IN ('pending', 'active', 'complete', 'error')),
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    anomaly_count       INTEGER NOT NULL DEFAULT 0 CHECK (anomaly_count >= 0),
    next_retry_at       TIMESTAMP,
    last_error_code     TEXT NOT NULL DEFAULT '',
    updated_at          TIMESTAMP NOT NULL,
    UNIQUE (binding_id, artifact_path, generation)
);

INSERT INTO usage_bindings_previous
SELECT id, session_id, harness, native_root_id, initial_model_id,
       state, last_error_code, updated_at, provider_hint
FROM usage_bindings
WHERE harness IN ('claude-code', 'codex');

INSERT INTO usage_sources_previous
SELECT source.id, source.binding_id, source.kind, source.native_session_id,
       source.subagent_id, source.artifact_path, source.file_identity,
       source.generation, source.byte_offset, source.parser_state_json,
       source.state, source.failure_count, source.anomaly_count,
       source.next_retry_at, source.last_error_code, source.updated_at
FROM usage_sources source
JOIN usage_bindings_previous binding ON binding.id = source.binding_id
WHERE source.kind IN ('claude_main', 'claude_subagent', 'codex_rollout');

DROP TABLE usage_sources;
DROP TABLE usage_bindings;
ALTER TABLE usage_bindings_previous RENAME TO usage_bindings;
ALTER TABLE usage_sources_previous RENAME TO usage_sources;

CREATE INDEX idx_usage_bindings_session_state ON usage_bindings (session_id, state);
CREATE INDEX idx_usage_sources_state_retry ON usage_sources (state, next_retry_at);
CREATE INDEX idx_usage_sources_binding_kind ON usage_sources (binding_id, kind);
CREATE INDEX idx_usage_sources_codex_native_latest
    ON usage_sources (kind, native_session_id, binding_id, generation DESC, id DESC);

CREATE TRIGGER usage_bindings_cdc_insert AFTER INSERT ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_update AFTER UPDATE ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_sources_cdc_update AFTER UPDATE ON usage_sources
WHEN OLD.anomaly_count IS NOT NEW.anomaly_count
  OR OLD.last_error_code IS NOT NEW.last_error_code
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ub.session_id, 'session_updated', json_object('id', ub.session_id), NEW.updated_at
    FROM usage_bindings ub JOIN sessions s ON s.id = ub.session_id WHERE ub.id = NEW.binding_id;
END;

COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
