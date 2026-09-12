-- +goose Up

-- `updated_at` tracks the most recent provider observation. A provider that
-- flaps between paused and resuming during one wake therefore cannot serve as
-- the start of the user-visible startup deadline. Preserve the beginning of a
-- running-intent transition independently so reconciliation can bound it.
ALTER TABLE ao_sandboxes
    ADD COLUMN startup_started_at TIMESTAMPTZ;

UPDATE ao_sandboxes
SET startup_started_at = updated_at
WHERE desired_state = 'running'
  AND observed_state IN ('provisioning', 'restoring', 'bootstrapping', 'failed');

-- +goose Down

ALTER TABLE ao_sandboxes
    DROP COLUMN startup_started_at;
