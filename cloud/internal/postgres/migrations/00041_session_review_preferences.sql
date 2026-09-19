-- +goose Up

-- Cloud session controls mirror the desktop inspector settings. Reviewer
-- harness is empty when the review should use the worker session's harness.
ALTER TABLE ao_sessions
	ADD COLUMN reviewer_harness TEXT NOT NULL DEFAULT '',
	ADD COLUMN auto_inject_ci BOOLEAN NOT NULL DEFAULT true,
	ADD COLUMN auto_inject_review BOOLEAN NOT NULL DEFAULT true,
	ADD COLUMN terminate_on_pr_merge BOOLEAN NOT NULL DEFAULT false;

-- Keep the provider which actually executed a review. A session's reviewer
-- setting is mutable, so deriving historic runs from the current setting would
-- show the wrong agent.
ALTER TABLE ao_review_runs
	ADD COLUMN harness TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE ao_review_runs DROP COLUMN harness;
ALTER TABLE ao_sessions
	DROP COLUMN terminate_on_pr_merge,
	DROP COLUMN auto_inject_review,
	DROP COLUMN auto_inject_ci,
	DROP COLUMN reviewer_harness;
