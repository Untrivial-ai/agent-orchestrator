-- Summary: persist the pull request head commit that a provider review reviewed.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE pr_reviews ADD COLUMN target_sha TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_pr_reviews_target_sha ON pr_reviews (pr_url, target_sha, submitted_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_pr_reviews_target_sha;
ALTER TABLE pr_reviews DROP COLUMN target_sha;
-- +goose StatementEnd
