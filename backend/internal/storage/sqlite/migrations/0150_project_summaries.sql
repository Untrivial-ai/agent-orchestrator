-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_summaries (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    source_watermark TEXT NOT NULL,
    narrative TEXT NOT NULL,
    projection_json TEXT NOT NULL,
    generated_at TIMESTAMP NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE project_summaries;
-- +goose StatementEnd
