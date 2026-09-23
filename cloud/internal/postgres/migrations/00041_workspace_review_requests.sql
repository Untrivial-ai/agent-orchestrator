-- The cloud workspace review API dispatches its file tree, search, diff,
-- revision, and write operations through the durable worker request queue.
-- Keep every workspace.review request kind valid for Docker, NodeOps, and
-- Coder workers, which all consume this shared transport.
-- +goose Up
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

-- +goose Down
DELETE FROM ao_worker_requests WHERE kind IN (
    'workspace.review.summary', 'workspace.review.tree',
    'workspace.review.search', 'workspace.review.file',
    'workspace.review.diffs', 'workspace.review.revision',
    'workspace.review.write'
);
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
