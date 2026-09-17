-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;

-- 0102 recorded how each canonical metric was obtained in four parallel
-- provenance columns and parked every counter outside the shared vocabulary in
-- a provider-shaped detail table. Both choices cost more than they bought: the
-- provenance columns only ever distinguished "known" from "unknown", which the
-- nullable counter already says, and a typed detail table silently drops any
-- field the provider adds later.
--
-- This rebuild replaces them with one event-level measurement kind and the
-- bounded provider usage object exactly as the CLI emitted it. It also folds
-- the four cost components into the three neutral ones the estimator now
-- produces: cache writes are non-cache-read input, so their charge belongs in
-- input_cost_nanos rather than in a component of its own.
CREATE TABLE model_usage_events_next (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id                  INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    usage_source_id             INTEGER NOT NULL REFERENCES usage_sources (id) ON DELETE CASCADE,
    provider_id                 TEXT NOT NULL CHECK (provider_id IN ('openai', 'anthropic')),
    billing_provider_id         TEXT CHECK (billing_provider_id IS NULL OR trim(billing_provider_id) <> ''),
    model_id                    TEXT NOT NULL CHECK (trim(model_id) <> ''),
    usage_measurement_kind      TEXT NOT NULL
        CHECK (usage_measurement_kind IN ('native_reported', 'ao_estimated', 'mixed', 'unknown')),
    input_tokens                INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    cached_input_tokens         INTEGER CHECK (cached_input_tokens IS NULL OR cached_input_tokens >= 0),
    uncached_input_tokens       INTEGER CHECK (uncached_input_tokens IS NULL OR uncached_input_tokens >= 0),
    output_tokens               INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    provider_usage_json         TEXT
        CHECK (provider_usage_json IS NULL
               OR (json_valid(provider_usage_json) AND json_type(provider_usage_json, '$') = 'object')),
    source_event_key            TEXT NOT NULL CHECK (trim(source_event_key) <> ''),
    created_at                  TIMESTAMP,
    input_cost_nanos            INTEGER CHECK (input_cost_nanos IS NULL OR input_cost_nanos >= 0),
    cached_input_cost_nanos     INTEGER CHECK (cached_input_cost_nanos IS NULL OR cached_input_cost_nanos >= 0),
    output_cost_nanos           INTEGER CHECK (output_cost_nanos IS NULL OR output_cost_nanos >= 0),
    estimated_cost_nanos        INTEGER CHECK (estimated_cost_nanos IS NULL OR estimated_cost_nanos >= 0),
    pricing_version             TEXT NOT NULL DEFAULT '',
    UNIQUE (binding_id, source_event_key),
    CHECK (input_tokens IS NULL OR cached_input_tokens IS NULL OR uncached_input_tokens IS NULL
           OR input_tokens = cached_input_tokens + uncached_input_tokens)
);

-- AO has never approximated a counter: every stored event came from a native
-- usage record, or from exact arithmetic over native cumulative counters, which
-- the target design still calls native_reported. So the only distinction the
-- retired provenance columns can still make is whether the event carried usable
-- counters at all.
--
-- provider_usage_json stays null here. Reconstructing it from the detail tables
-- would fabricate a "bounded provider object" AO never observed; the legacy
-- repairer refills it from the durable transcript when one still matches.
INSERT INTO model_usage_events_next (
    id, binding_id, usage_source_id, provider_id, billing_provider_id, model_id,
    usage_measurement_kind,
    input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
    provider_usage_json, source_event_key, created_at,
    input_cost_nanos, cached_input_cost_nanos, output_cost_nanos,
    estimated_cost_nanos, pricing_version
)
SELECT
    event.id, event.binding_id, event.usage_source_id, event.provider_id,
    event.billing_provider_id, event.model_id,
    CASE WHEN 'unknown' IN (
             event.input_provenance, event.cached_input_provenance,
             event.uncached_input_provenance, event.output_provenance
         ) THEN 'unknown' ELSE 'native_reported' END,
    event.input_tokens, event.cached_input_tokens, event.uncached_input_tokens,
    event.output_tokens,
    NULL, event.source_event_key, event.created_at,
    -- A component is a lower bound the aggregate still sums, so a half-known
    -- input charge must stay unknown rather than under-report as its fresh part.
    CASE WHEN event.uncached_input_cost_nanos IS NOT NULL
          AND event.cache_write_cost_nanos IS NOT NULL
         THEN event.uncached_input_cost_nanos + event.cache_write_cost_nanos END,
    event.cache_read_cost_nanos, event.output_cost_nanos,
    event.estimated_cost_nanos, event.pricing_version
FROM model_usage_events event;

DROP TABLE openai_usage_event_details;
DROP TABLE anthropic_usage_event_details;
DROP TABLE model_usage_events;
ALTER TABLE model_usage_events_next RENAME TO model_usage_events;

-- 0102's idx_model_usage_events_provider is deliberately not recreated. It
-- existed to reach the provider detail tables this migration drops; provider_id
-- now only selects which shape to read provider_usage_json as, and no query
-- filters or groups by it.
CREATE INDEX idx_model_usage_events_binding_model ON model_usage_events (binding_id, model_id);
CREATE INDEX idx_model_usage_events_usage_source ON model_usage_events (usage_source_id);
CREATE INDEX idx_model_usage_events_cost_candidates
    ON model_usage_events (billing_provider_id, pricing_version, id)
    WHERE estimated_cost_nanos IS NULL;
CREATE INDEX idx_model_usage_events_canonical_cost_candidates
    ON model_usage_events (
        CASE lower(trim(billing_provider_id))
            WHEN 'z.ai' THEN 'zai'
            ELSE lower(trim(billing_provider_id))
        END,
        id
    )
    WHERE billing_provider_id IS NOT NULL
      AND estimated_cost_nanos IS NULL;

COMMIT;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally a no-op, like 0110. Rebuilding the detail tables would have to
-- invent the per-provider counters this migration deliberately stopped
-- collecting, and 0102's own Down already drops them again, so downgrading
-- below 0112 restores nothing an older binary can use.
SELECT 1;
-- +goose StatementEnd
