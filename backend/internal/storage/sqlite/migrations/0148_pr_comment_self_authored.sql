-- +goose Up
ALTER TABLE pr_comment ADD COLUMN is_self_authored INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE pr_comment DROP COLUMN is_self_authored;
