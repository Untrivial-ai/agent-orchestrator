-- Record whether a review pass was started manually or by auto-review.

-- +goose Up
ALTER TABLE review_run ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual';

-- +goose Down
ALTER TABLE review_run DROP COLUMN trigger_source;
