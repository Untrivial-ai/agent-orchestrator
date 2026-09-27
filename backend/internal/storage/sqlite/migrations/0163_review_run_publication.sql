-- Issue #5701: reviewers submit one result to AO and the daemon publishes the
-- GitHub review, so each review run records its inline findings and the state
-- of the daemon-side publication attempt.

-- +goose Up
ALTER TABLE review_run ADD COLUMN findings TEXT NOT NULL DEFAULT '';
ALTER TABLE review_run ADD COLUMN publish_state TEXT NOT NULL DEFAULT 'pending' CHECK (publish_state IN ('pending', 'publishing', 'published', 'failed', 'uncertain'));
ALTER TABLE review_run ADD COLUMN publish_error TEXT NOT NULL DEFAULT '';
-- Runs completed under the old contract were published by the reviewer itself;
-- an id proves the GitHub review exists.
UPDATE review_run SET publish_state = 'published' WHERE github_review_id != '';

-- +goose Down
ALTER TABLE review_run DROP COLUMN findings;
ALTER TABLE review_run DROP COLUMN publish_state;
ALTER TABLE review_run DROP COLUMN publish_error;
