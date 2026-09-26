-- +goose Up
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker', 'nodeops', 'coder', 'freestyle'));

-- +goose Down
-- A live Freestyle VM cannot be represented by the previous constraint.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker', 'nodeops', 'coder'));
