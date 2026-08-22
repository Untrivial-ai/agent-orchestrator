-- +goose Up
-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_model_usage_events_canonical_cost_candidates;
-- +goose StatementEnd
