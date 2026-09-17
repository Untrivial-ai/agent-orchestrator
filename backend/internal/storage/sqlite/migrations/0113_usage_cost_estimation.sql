-- +goose Up
-- +goose StatementBegin
ALTER TABLE usage_bindings
    ADD COLUMN provider_hint TEXT NOT NULL DEFAULT '';

-- 0102 normalized every event onto a provider *vocabulary* (openai or
-- anthropic) so the right detail table applies. Billing is a separate fact: an
-- Anthropic-vocabulary transcript can be served by z.ai, and pricing must never
-- be derived from the harness. Keep the two apart instead of widening
-- provider_id, which would break the "details match provider_id" invariant.
ALTER TABLE model_usage_events
    ADD COLUMN billing_provider_id TEXT
        CHECK (billing_provider_id IS NULL OR trim(billing_provider_id) <> '');
ALTER TABLE model_usage_events
    ADD COLUMN uncached_input_cost_nanos INTEGER
        CHECK (uncached_input_cost_nanos IS NULL OR uncached_input_cost_nanos >= 0);
ALTER TABLE model_usage_events
    ADD COLUMN cache_read_cost_nanos INTEGER
        CHECK (cache_read_cost_nanos IS NULL OR cache_read_cost_nanos >= 0);
ALTER TABLE model_usage_events
    ADD COLUMN cache_write_cost_nanos INTEGER
        CHECK (cache_write_cost_nanos IS NULL OR cache_write_cost_nanos >= 0);
ALTER TABLE model_usage_events
    ADD COLUMN output_cost_nanos INTEGER
        CHECK (output_cost_nanos IS NULL OR output_cost_nanos >= 0);
ALTER TABLE model_usage_events
    ADD COLUMN estimated_cost_nanos INTEGER
        CHECK (estimated_cost_nanos IS NULL OR estimated_cost_nanos >= 0);
ALTER TABLE model_usage_events
    ADD COLUMN pricing_version TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_model_usage_events_cost_candidates
    ON model_usage_events (billing_provider_id, pricing_version, id)
    WHERE estimated_cost_nanos IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- This migration is intentionally additive. Rebuilding both durable usage
-- tables to remove nullable/defaulted columns would add downgrade data-loss
-- risk without restoring any behavior required by an older binary.
SELECT 1;
-- +goose StatementEnd
