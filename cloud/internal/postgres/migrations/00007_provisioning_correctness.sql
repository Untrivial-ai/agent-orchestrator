-- +goose Up

-- Provider-side auto-pause is disabled. Keep this legacy column at zero so an
-- older reader can still scan the row during a rolling deployment without
-- re-enabling provider idling.
UPDATE ao_sandboxes SET auto_stop_minutes = 0;

ALTER TABLE ao_sandboxes
    ALTER COLUMN auto_stop_minutes SET DEFAULT 0;

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_auto_stop_minutes_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_auto_stop_minutes_check
    CHECK (auto_stop_minutes >= 0);

-- +goose Down
UPDATE ao_sandboxes SET auto_stop_minutes = 30 WHERE auto_stop_minutes = 0;

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_auto_stop_minutes_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_auto_stop_minutes_check
    CHECK (auto_stop_minutes > 0);

ALTER TABLE ao_sandboxes
    ALTER COLUMN auto_stop_minutes SET DEFAULT 30;
