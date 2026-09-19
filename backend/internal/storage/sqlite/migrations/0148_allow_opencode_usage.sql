-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- opencode is the only agent harness AO ships, so the usage binding lifecycle
-- must be able to record an 'opencode' route. SQLite cannot widen a CHECK
-- constraint in place, so rebuild usage_bindings with the same table-copy
-- pattern 0117 used. usage_sources is deliberately left alone: opencode has no
-- certified transcript pipeline, so it registers no sources and its kind CHECK
-- does not need the new value.
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;

CREATE TABLE usage_bindings_next (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (harness IN ('claude-code', 'codex', 'kimi', 'opencode')),
    native_root_id     TEXT NOT NULL CHECK (trim(native_root_id) <> ''),
    initial_model_id   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('discovering', 'active', 'finalizing', 'complete', 'partial')),
    last_error_code    TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMP NOT NULL,
    provider_hint      TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, harness, native_root_id)
);

INSERT INTO usage_bindings_next
SELECT id, session_id, harness, native_root_id, initial_model_id,
       state, last_error_code, updated_at, provider_hint
FROM usage_bindings;

DROP TABLE usage_bindings;
ALTER TABLE usage_bindings_next RENAME TO usage_bindings;

CREATE INDEX idx_usage_bindings_session_state ON usage_bindings (session_id, state);

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

DELETE FROM usage_sources
WHERE binding_id IN (SELECT id FROM usage_bindings WHERE harness = 'opencode');
DELETE FROM model_usage_events
WHERE binding_id IN (SELECT id FROM usage_bindings WHERE harness = 'opencode');

CREATE TABLE usage_bindings_previous (
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

INSERT INTO usage_bindings_previous
SELECT id, session_id, harness, native_root_id, initial_model_id,
       state, last_error_code, updated_at, provider_hint
FROM usage_bindings
WHERE harness IN ('claude-code', 'codex', 'kimi');

DROP TABLE usage_bindings;
ALTER TABLE usage_bindings_previous RENAME TO usage_bindings;

CREATE INDEX idx_usage_bindings_session_state ON usage_bindings (session_id, state);

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

COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
