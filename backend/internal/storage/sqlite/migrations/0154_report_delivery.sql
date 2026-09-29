-- +goose Up
-- +goose StatementBegin
CREATE TABLE report_worker_interrupts (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    last_interrupted_at TIMESTAMP NOT NULL
);

ALTER TABLE reports ADD COLUMN delivery_batch_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_reports_project_delivery
    ON reports(project_id, delivery_state, created_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_project_delivery;
DROP TABLE IF EXISTS report_worker_interrupts;
ALTER TABLE reports DROP COLUMN delivery_batch_id;
-- +goose StatementEnd
