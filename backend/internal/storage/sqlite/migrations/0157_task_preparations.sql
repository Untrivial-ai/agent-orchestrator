-- +goose Up
ALTER TABLE sessions ADD COLUMN is_task_preparation INTEGER NOT NULL DEFAULT 0
    CHECK (is_task_preparation IN (0, 1));

-- +goose Down
ALTER TABLE sessions DROP COLUMN is_task_preparation;
