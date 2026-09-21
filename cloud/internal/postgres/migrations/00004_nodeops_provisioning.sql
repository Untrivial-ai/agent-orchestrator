-- +goose Up
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker', 'nodeops'));

ALTER TABLE ao_sandboxes
    ADD COLUMN bootstrap_context JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(bootstrap_context) = 'object');

-- +goose Down
ALTER TABLE ao_sandboxes
    DROP COLUMN IF EXISTS bootstrap_context;

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker'));
