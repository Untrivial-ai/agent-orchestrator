-- +goose Up
ALTER TABLE task_delegations ADD COLUMN startup_state TEXT NOT NULL DEFAULT 'legacy'
    CHECK (startup_state IN ('legacy', 'seeded', 'starting', 'ready'));
UPDATE task_delegations SET startup_state = CASE
    WHEN state = 'pending' THEN 'seeded' ELSE 'starting' END
    WHERE recoverable = 1;
CREATE INDEX task_delegations_worker_idx ON task_delegations(worker_id) WHERE worker_id IS NOT NULL;

-- +goose Down
DROP INDEX task_delegations_worker_idx;
ALTER TABLE task_delegations DROP COLUMN startup_state;
