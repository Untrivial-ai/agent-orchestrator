-- +goose Up

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

DELETE FROM ao_worker_requests WHERE kind LIKE 'interface.%';
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch'
    ));
