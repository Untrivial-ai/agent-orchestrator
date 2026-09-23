-- +goose Up

-- Coder is a first-class sandbox provider. Keep the database constraint in
-- sync with the provider registry so session creation can persist its sandbox
-- row before the reconciler provisions the Coder workspace.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker', 'nodeops', 'coder'));

-- +goose Down

-- A Coder sandbox cannot be represented by the prior schema. Fail the rollback
-- instead of silently relabelling live provider resources.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_provider_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_provider_check
    CHECK (provider IN ('ecs', 'daytona', 'docker', 'nodeops'));
