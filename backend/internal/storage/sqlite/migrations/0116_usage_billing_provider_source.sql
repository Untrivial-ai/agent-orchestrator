-- +goose Up
-- +goose StatementBegin
-- Claude transcripts name no provider, so an event collected before its first
-- route-bearing hook can only be attributed by the model that answered. That is
-- evidence, but weaker evidence: Bedrock and Vertex are routes the collector
-- accepts and the catalog does not list, and both serve Claude model names that
-- a catalog lookup finds only under anthropic.
--
-- Recording how an attribution was reached is what makes that acceptable. An
-- observation — a provider named by the transcript or by the trusted route hint
-- — stays immutable. An inference may be replaced later, exactly once, by an
-- observation, together with the cost derived from it. Without this column the
-- write-once billing_provider_id and immutable estimated_cost_nanos would make
-- a wrong guess permanent and unreachable by any later repair.
ALTER TABLE model_usage_events
    ADD COLUMN billing_provider_source TEXT
        CHECK (billing_provider_source IS NULL
               OR billing_provider_source IN ('observed', 'inferred'));

-- Every row attributed before this column existed came from the transcript or
-- the route hint; the model fallback is newer than the last shipped migration.
UPDATE model_usage_events
SET billing_provider_source = 'observed'
WHERE billing_provider_id IS NOT NULL;

-- Repair and correction both scan for rows that are still open: unattributed,
-- or attributed only by inference.
CREATE INDEX idx_model_usage_events_open_attribution
    ON model_usage_events (usage_source_id, id)
    WHERE billing_provider_id IS NULL OR billing_provider_source = 'inferred';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally additive, like 0108. Dropping the column would discard the only
-- record of which attributions are safe to revise, and an older binary neither
-- reads it nor is harmed by it.
SELECT 1;
-- +goose StatementEnd
