-- +goose Up
-- The PR discussion count/commenters feature (#4383) was reverted; drop the
-- columns 0159 and 0160 added.
ALTER TABLE pr DROP COLUMN discussion_commenters_json;
ALTER TABLE pr DROP COLUMN discussion_comment_count;

-- +goose Down
ALTER TABLE pr ADD COLUMN discussion_comment_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pr ADD COLUMN discussion_commenters_json TEXT NOT NULL DEFAULT '[]';
