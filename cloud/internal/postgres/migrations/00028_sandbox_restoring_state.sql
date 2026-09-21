-- +goose Up

-- A paused NodeOps sandbox resumes its preserved root filesystem. Record the
-- short period between provider resume acceptance and the one-time in-place
-- worker refresh so a released worker binary cannot remain stale indefinitely.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'restoring', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed', 'deleting', 'deleted'
    ));

-- +goose Down

UPDATE ao_sandboxes
    SET observed_state = 'bootstrapping'
    WHERE observed_state = 'restoring';

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed', 'deleting', 'deleted'
    ));
