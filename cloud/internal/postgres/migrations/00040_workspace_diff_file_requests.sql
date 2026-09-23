-- Docker-backed per-file diff reads use a distinct durable transport kind so
-- NodeOps and Coder workers cannot accidentally receive an unsupported request.
-- +goose Up
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'workspace.diff-file',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch'
    ));

-- +goose Down
DELETE FROM ao_worker_requests WHERE kind = 'workspace.diff-file';
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch'
    ));
