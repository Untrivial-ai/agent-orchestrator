-- +goose Up
-- Agent switching was removed from AO. These tables only ever backed that saga
-- and its failure-observability pipeline; nothing on the retained paths reads
-- them. Drop children before parents so no foreign key blocks the teardown
-- (agent_switch_failure_receipts -> agent_switches -> agent_native_sessions).
-- Associated CDC/scope triggers are dropped automatically with their table.
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_switch_failure_receipts;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_switch_failure_outbox;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_switch_failure_delivery_state;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_switch_failure_policy;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_switches;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_native_sessions;
-- +goose StatementEnd

-- +goose Down
-- The removed feature has no supported downgrade path.
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
