-- +goose Up

-- The reconciler's terminal verdict for a worker that never started within the
-- startup ceiling is recorded as observed_state 'terminated'
-- (domain.SandboxObservedTerminated, reconciler.go terminateSandbox). That value
-- was added in code but never allowed by the CHECK constraint, so every such
-- write failed with SQLSTATE 23514 ("violates check constraint
-- ao_sandboxes_observed_state_check"). The sandbox could then never settle to its
-- terminal state: the reconciler retried the same row every tick, and the
-- session's terminal stayed stuck reattaching on "worker not connected". Allow
-- 'terminated' so the verdict persists.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'restoring', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed', 'terminated', 'deleting', 'deleted'
    ));

-- +goose Down

-- Fold terminated rows back into 'failed' so the pre-terminated constraint holds.
UPDATE ao_sandboxes
    SET observed_state = 'failed'
    WHERE observed_state = 'terminated';

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'restoring', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed', 'deleting', 'deleted'
    ));
