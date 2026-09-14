-- +goose Up
-- +goose StatementBegin
-- Retention deletes old CDC rows by timestamp. Keep that maintenance query
-- indexed so it does not turn into a full-table scan as the log grows.
CREATE INDEX idx_change_log_created_at_seq ON change_log (created_at, seq);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_change_log_created_at_seq;
-- +goose StatementEnd
