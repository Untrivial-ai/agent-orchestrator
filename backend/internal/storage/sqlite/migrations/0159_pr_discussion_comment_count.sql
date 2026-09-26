-- +goose Up
ALTER TABLE pr ADD COLUMN discussion_comment_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE pr DROP COLUMN discussion_comment_count;
