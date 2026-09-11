-- +goose Up

-- The browser proxy dispatches 'browser.fetch' worker requests, but the kind
-- was never added to ao_worker_requests_kind_check (pre-existing on main;
-- staging carries a hand-patched schema), so every browser fetch returned
-- HTTP 422 on a fresh database. Environments that already applied 00035 before
-- this migration landed re-run only 00032, so the list also carries the
-- interface.* kinds instead of dropping them and breaking in-flight handoffs.

ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch',
        'interface.inspect', 'interface.interrupt', 'interface.stop',
        'interface.native-id', 'interface.start'
    ));

-- +goose Down

DELETE FROM ao_worker_requests WHERE kind = 'browser.fetch';
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close'
    ));