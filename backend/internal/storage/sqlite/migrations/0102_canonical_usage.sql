-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;

-- Some development profiles recorded the earlier usage migration without its
-- physical tables. Recreate its durable collection boundary before rebuilding
-- the event table so those profiles converge too.
CREATE TABLE IF NOT EXISTS usage_bindings (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (harness IN ('claude-code', 'codex')),
    native_root_id     TEXT NOT NULL CHECK (trim(native_root_id) <> ''),
    initial_model_id   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('discovering', 'active', 'finalizing', 'complete', 'partial')),
    last_error_code    TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMP NOT NULL,
    UNIQUE (session_id, harness, native_root_id)
);

CREATE TABLE IF NOT EXISTS usage_sources (
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

CREATE TABLE IF NOT EXISTS model_usage_events (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id              INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    usage_source_id         INTEGER NOT NULL REFERENCES usage_sources (id) ON DELETE CASCADE,
    model_id                TEXT NOT NULL CHECK (trim(model_id) <> ''),
    input_tokens            INTEGER NOT NULL CHECK (input_tokens >= 0),
    uncached_input_tokens   INTEGER NOT NULL CHECK (uncached_input_tokens >= 0 AND uncached_input_tokens <= input_tokens),
    cache_read_tokens       INTEGER NOT NULL CHECK (cache_read_tokens >= 0 AND cache_read_tokens <= input_tokens),
    cache_write_tokens      INTEGER NOT NULL CHECK (cache_write_tokens >= 0 AND cache_write_tokens <= input_tokens),
    output_tokens           INTEGER NOT NULL CHECK (output_tokens >= 0),
    reasoning_tokens        INTEGER CHECK (reasoning_tokens IS NULL OR (reasoning_tokens >= 0 AND reasoning_tokens <= output_tokens)),
    source_event_key        TEXT NOT NULL CHECK (trim(source_event_key) <> ''),
    UNIQUE (binding_id, source_event_key)
);

-- SQLite cannot replace the legacy event constraints or split provider-native
-- counters in place. This is a structural one-to-one table rebuild: it copies
-- the persisted counters without rereading transcripts or running a historical
-- usage backfill.
CREATE TABLE model_usage_events_next (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id                  INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    usage_source_id             INTEGER NOT NULL REFERENCES usage_sources (id) ON DELETE CASCADE,
    provider_id                 TEXT NOT NULL CHECK (provider_id IN ('openai', 'anthropic')),
    model_id                    TEXT NOT NULL CHECK (trim(model_id) <> ''),
    input_tokens                INTEGER,
    input_provenance            TEXT NOT NULL CHECK (input_provenance IN ('reported', 'derived', 'unsupported', 'unknown')),
    cached_input_tokens         INTEGER,
    cached_input_provenance     TEXT NOT NULL CHECK (cached_input_provenance IN ('reported', 'derived', 'unsupported', 'unknown')),
    uncached_input_tokens       INTEGER,
    uncached_input_provenance   TEXT NOT NULL CHECK (uncached_input_provenance IN ('reported', 'derived', 'unsupported', 'unknown')),
    output_tokens               INTEGER,
    output_provenance           TEXT NOT NULL CHECK (output_provenance IN ('reported', 'derived', 'unsupported', 'unknown')),
    source_event_key            TEXT NOT NULL CHECK (trim(source_event_key) <> ''),
    created_at                  TIMESTAMP,
    UNIQUE (binding_id, source_event_key),
    CHECK ((input_tokens IS NULL AND input_provenance = 'unknown') OR (input_tokens >= 0 AND input_provenance <> 'unknown')),
    CHECK ((cached_input_tokens IS NULL AND cached_input_provenance = 'unknown') OR (cached_input_tokens >= 0 AND cached_input_provenance <> 'unknown')),
    CHECK ((uncached_input_tokens IS NULL AND uncached_input_provenance = 'unknown') OR (uncached_input_tokens >= 0 AND uncached_input_provenance <> 'unknown')),
    CHECK ((output_tokens IS NULL AND output_provenance = 'unknown') OR (output_tokens >= 0 AND output_provenance <> 'unknown')),
    CHECK (input_tokens IS NULL OR cached_input_tokens IS NULL OR uncached_input_tokens IS NULL OR input_tokens = cached_input_tokens + uncached_input_tokens)
);

INSERT INTO model_usage_events_next (
    id, binding_id, usage_source_id, provider_id, model_id,
    input_tokens, input_provenance,
    cached_input_tokens, cached_input_provenance,
    uncached_input_tokens, uncached_input_provenance,
    output_tokens, output_provenance,
    source_event_key, created_at
)
SELECT
    event.id, event.binding_id, event.usage_source_id,
    CASE binding.harness WHEN 'codex' THEN 'openai' ELSE 'anthropic' END,
    event.model_id,
    event.input_tokens,
    CASE binding.harness WHEN 'codex' THEN 'reported' ELSE 'derived' END,
    event.cache_read_tokens, 'reported',
    event.uncached_input_tokens + event.cache_write_tokens, 'derived',
    event.output_tokens, 'reported',
    event.source_event_key, NULL
FROM model_usage_events event
JOIN usage_bindings binding ON binding.id = event.binding_id;

CREATE TABLE openai_usage_event_details (
    event_id                           INTEGER PRIMARY KEY REFERENCES model_usage_events_next (id) ON DELETE CASCADE,
    openai_reasoning_output_tokens     INTEGER CHECK (openai_reasoning_output_tokens IS NULL OR openai_reasoning_output_tokens >= 0),
    openai_cache_write_input_tokens    INTEGER CHECK (openai_cache_write_input_tokens IS NULL OR openai_cache_write_input_tokens >= 0),
    openai_reported_total_tokens       INTEGER CHECK (openai_reported_total_tokens IS NULL OR openai_reported_total_tokens >= 0)
);

CREATE TABLE anthropic_usage_event_details (
    event_id                                      INTEGER PRIMARY KEY REFERENCES model_usage_events_next (id) ON DELETE CASCADE,
    anthropic_direct_uncached_input_tokens        INTEGER CHECK (anthropic_direct_uncached_input_tokens IS NULL OR anthropic_direct_uncached_input_tokens >= 0),
    anthropic_cache_creation_input_tokens         INTEGER CHECK (anthropic_cache_creation_input_tokens IS NULL OR anthropic_cache_creation_input_tokens >= 0),
    anthropic_cache_creation_5m_input_tokens      INTEGER CHECK (anthropic_cache_creation_5m_input_tokens IS NULL OR anthropic_cache_creation_5m_input_tokens >= 0),
    anthropic_cache_creation_1h_input_tokens      INTEGER CHECK (anthropic_cache_creation_1h_input_tokens IS NULL OR anthropic_cache_creation_1h_input_tokens >= 0)
);

INSERT INTO openai_usage_event_details (
    event_id, openai_reasoning_output_tokens, openai_cache_write_input_tokens,
    openai_reported_total_tokens
)
SELECT event.id, event.reasoning_tokens, event.cache_write_tokens, NULL
FROM model_usage_events event
JOIN usage_bindings binding ON binding.id = event.binding_id
WHERE binding.harness = 'codex';

INSERT INTO anthropic_usage_event_details (
    event_id, anthropic_direct_uncached_input_tokens,
    anthropic_cache_creation_input_tokens,
    anthropic_cache_creation_5m_input_tokens,
    anthropic_cache_creation_1h_input_tokens
)
SELECT event.id, event.uncached_input_tokens, event.cache_write_tokens, NULL, NULL
FROM model_usage_events event
JOIN usage_bindings binding ON binding.id = event.binding_id
WHERE binding.harness = 'claude-code';

DROP TABLE model_usage_events;
ALTER TABLE model_usage_events_next RENAME TO model_usage_events;

CREATE INDEX idx_model_usage_events_binding_model ON model_usage_events (binding_id, model_id);
CREATE INDEX idx_model_usage_events_usage_source ON model_usage_events (usage_source_id);
CREATE INDEX idx_model_usage_events_provider ON model_usage_events (provider_id);
CREATE INDEX IF NOT EXISTS idx_usage_bindings_session_state ON usage_bindings (session_id, state);
CREATE INDEX IF NOT EXISTS idx_usage_sources_state_retry ON usage_sources (state, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_usage_sources_binding_kind ON usage_sources (binding_id, kind);
CREATE INDEX IF NOT EXISTS idx_usage_sources_codex_native_latest
    ON usage_sources (kind, native_session_id, binding_id, generation DESC, id DESC);


COMMIT;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;

CREATE TABLE model_usage_events_previous (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id              INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    usage_source_id         INTEGER NOT NULL REFERENCES usage_sources (id) ON DELETE CASCADE,
    model_id                TEXT NOT NULL CHECK (trim(model_id) <> ''),
    input_tokens            INTEGER NOT NULL CHECK (input_tokens >= 0),
    uncached_input_tokens   INTEGER NOT NULL CHECK (uncached_input_tokens >= 0 AND uncached_input_tokens <= input_tokens),
    cache_read_tokens       INTEGER NOT NULL CHECK (cache_read_tokens >= 0 AND cache_read_tokens <= input_tokens),
    cache_write_tokens      INTEGER NOT NULL CHECK (cache_write_tokens >= 0 AND cache_write_tokens <= input_tokens),
    output_tokens           INTEGER NOT NULL CHECK (output_tokens >= 0),
    reasoning_tokens        INTEGER CHECK (reasoning_tokens IS NULL OR (reasoning_tokens >= 0 AND reasoning_tokens <= output_tokens)),
    source_event_key        TEXT NOT NULL CHECK (trim(source_event_key) <> ''),
    UNIQUE (binding_id, source_event_key)
);

INSERT INTO model_usage_events_previous
SELECT event.id, event.binding_id, event.usage_source_id, event.model_id,
       COALESCE(event.input_tokens, 0),
       MAX(COALESCE(event.uncached_input_tokens, 0) - COALESCE(openai.openai_cache_write_input_tokens, anthropic.anthropic_cache_creation_input_tokens, 0), 0),
       COALESCE(event.cached_input_tokens, 0),
       COALESCE(openai.openai_cache_write_input_tokens, anthropic.anthropic_cache_creation_input_tokens, 0),
       COALESCE(event.output_tokens, 0), openai.openai_reasoning_output_tokens,
       event.source_event_key
FROM model_usage_events event
LEFT JOIN openai_usage_event_details openai ON openai.event_id = event.id
LEFT JOIN anthropic_usage_event_details anthropic ON anthropic.event_id = event.id;

DROP TABLE openai_usage_event_details;
DROP TABLE anthropic_usage_event_details;
DROP TABLE model_usage_events;
ALTER TABLE model_usage_events_previous RENAME TO model_usage_events;
CREATE INDEX idx_model_usage_events_binding_model ON model_usage_events (binding_id, model_id);
CREATE INDEX idx_model_usage_events_usage_source ON model_usage_events (usage_source_id);

COMMIT;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
