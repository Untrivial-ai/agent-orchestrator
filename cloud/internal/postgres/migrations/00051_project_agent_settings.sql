-- +goose Up
ALTER TABLE ao_sessions ADD COLUMN agent_config jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(agent_config) = 'object');
ALTER TABLE ao_review_runs ADD COLUMN reviewer_config jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(reviewer_config) = 'object');

-- +goose Down
ALTER TABLE ao_review_runs DROP COLUMN reviewer_config;
ALTER TABLE ao_sessions DROP COLUMN agent_config;
