-- +goose Up
ALTER TABLE pr ADD COLUMN discussion_commenters_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE pr DROP COLUMN discussion_commenters_json;
