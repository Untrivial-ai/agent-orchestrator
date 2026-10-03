-- +goose Up

-- Preserve a Chat selector change even when the user switches back to the
-- native terminal before sending another turn. The latest completed handoff
-- also restores this choice when the worker is replaced.
ALTER TABLE ao_interface_transitions
    ADD COLUMN selected_model TEXT NOT NULL DEFAULT '',
    ADD COLUMN selected_effort TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE ao_interface_transitions
    DROP COLUMN selected_model,
    DROP COLUMN selected_effort;
