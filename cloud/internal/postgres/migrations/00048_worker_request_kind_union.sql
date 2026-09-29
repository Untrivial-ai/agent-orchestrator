-- Repair the durable worker-request constraint after the workspace-review and
-- reviewer-harness migrations independently replaced it. Every cloud sandbox
-- provider consumes this shared request queue, so the constraint must preserve
-- the union of both feature sets.
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
        'browser.fetch', 'harness.inspect', 'harness.install'
    )) NOT VALID;

-- +goose Down
DELETE FROM ao_worker_requests WHERE kind IN ('harness.inspect', 'harness.install');
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
    )) NOT VALID;
