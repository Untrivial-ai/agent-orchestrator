-- +goose Up
-- +goose StatementBegin
ALTER TABLE pr_comment
    ADD COLUMN review_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE pr_comment DROP COLUMN review_id;
-- +goose StatementEnd
