-- +goose Up

-- Interface handoff commands use the durable worker request queue. Keep every
-- workspace and terminal kind already allowed by migration 00041.
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'workspace.diff-file',
        'workspace.review.summary', 'workspace.review.tree',
        'workspace.review.search', 'workspace.review.file',
        'workspace.review.diffs', 'workspace.review.revision',
        'workspace.review.write',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch',
        'interface.inspect', 'interface.interrupt', 'interface.stop',
        'interface.native-id', 'interface.start', 'interface.ready'
    ));

-- +goose Down

DELETE FROM ao_worker_requests WHERE kind LIKE 'interface.%';
ALTER TABLE ao_worker_requests
    DROP CONSTRAINT ao_worker_requests_kind_check;
ALTER TABLE ao_worker_requests
    ADD CONSTRAINT ao_worker_requests_kind_check
    CHECK (kind IN (
        'workspace.list', 'workspace.read', 'workspace.write', 'workspace.diff',
        'workspace.diff-file',
        'workspace.review.summary', 'workspace.review.tree',
        'workspace.review.search', 'workspace.review.file',
        'workspace.review.diffs', 'workspace.review.revision',
        'workspace.review.write',
        'terminal.open', 'terminal.input', 'terminal.resize', 'terminal.close',
        'browser.fetch'
    ));
