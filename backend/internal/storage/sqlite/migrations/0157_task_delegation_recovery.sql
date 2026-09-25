-- +goose Up
ALTER TABLE task_delegations ADD COLUMN recoverable INTEGER NOT NULL DEFAULT 0
    CHECK (recoverable IN (0, 1));

-- +goose Down
ALTER TABLE task_delegations DROP COLUMN recoverable;
