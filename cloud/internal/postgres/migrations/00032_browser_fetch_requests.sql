-- The desktop Browser panel proxies page loads through the worker transport as
-- 'browser.fetch' requests (httpapi/browser_handlers.go), but the kind check
-- was last widened for terminal.resize (00011) and never learned the browser
-- kind, so every proxied fetch failed the constraint on a fresh database.
-- +goose Up
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch'
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
