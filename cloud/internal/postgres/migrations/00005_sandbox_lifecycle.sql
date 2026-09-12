-- +goose Up

-- The reconciler expresses user intent as `paused` and `deleted` in addition to
-- the original `running`/`stopped` pair, and reports the two provider-side
-- teardown states it can observe.
ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_desired_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_desired_state_check
    CHECK (desired_state IN ('running', 'stopped', 'paused', 'deleted'));

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed', 'deleting', 'deleted'
    ));

-- The sandbox reconciler has no user principal and must scan due sandboxes
-- across every organization, so the tenant policy cannot serve it. The service
-- context is granted on ao_sandboxes alone: the reconciler claims rows here,
-- then performs all per-sandbox work inside an org-scoped transaction. The
-- runtime role remains non-superuser and non-BYPASSRLS.
-- +goose StatementBegin
CREATE FUNCTION ao_service_context() RETURNS BOOLEAN
LANGUAGE sql
STABLE
AS $$
    SELECT coalesce(current_setting('ao.service', true), '') = 'control-plane'
$$;
-- +goose StatementEnd

CREATE POLICY ao_sandboxes_service_policy ON ao_sandboxes
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- A worker redeeming its bootstrap ticket presents nothing but the token, so
-- the lookup is by token hash across organizations. The hash is the
-- authorization; the redeeming transaction learns the org and every subsequent
-- statement is org-scoped again.
CREATE POLICY ao_access_tickets_service_policy ON ao_access_tickets
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- Worker connection epochs are globally monotonic so a token minted for a
-- replaced sandbox can never be mistaken for the current one.
CREATE SEQUENCE ao_worker_epoch_sequence AS BIGINT START WITH 1 INCREMENT BY 1;

-- +goose Down
DROP SEQUENCE IF EXISTS ao_worker_epoch_sequence;

DROP POLICY IF EXISTS ao_access_tickets_service_policy ON ao_access_tickets;

DROP POLICY IF EXISTS ao_sandboxes_service_policy ON ao_sandboxes;

DROP FUNCTION IF EXISTS ao_service_context();

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_observed_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_observed_state_check
    CHECK (observed_state IN (
        'requested', 'provisioning', 'bootstrapping', 'ready', 'running',
        'stopping', 'stopped', 'disconnected', 'failed'
    ));

ALTER TABLE ao_sandboxes
    DROP CONSTRAINT IF EXISTS ao_sandboxes_desired_state_check;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_desired_state_check
    CHECK (desired_state IN ('running', 'stopped'));
