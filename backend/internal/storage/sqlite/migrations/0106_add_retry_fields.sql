-- +goose Up
-- +goose StatementBegin
ALTER TABLE task_runs ADD COLUMN previous_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN retry_mode TEXT NOT NULL DEFAULT '' CHECK (retry_mode IN ('', 'resume', 'fresh'));
CREATE UNIQUE INDEX idx_run_reviews_run_unique ON run_reviews(run_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_run_reviews_run_unique;
ALTER TABLE task_runs DROP COLUMN retry_mode;
ALTER TABLE task_runs DROP COLUMN previous_run_id;
-- +goose StatementEnd
