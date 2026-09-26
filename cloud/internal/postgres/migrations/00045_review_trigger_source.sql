-- +goose Up

ALTER TABLE ao_review_runs
	ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual'
		CHECK (trigger_source IN ('manual', 'auto'));

-- Automatic scanning is periodic and may run on multiple control-plane
-- replicas. A commit gets one automatic review, while manual retries remain
-- available after that run resolves.
CREATE UNIQUE INDEX ao_review_runs_auto_pull_request_sha_idx
	ON ao_review_runs (pull_request_id, target_sha)
	WHERE trigger_source = 'auto';

-- +goose Down

DROP INDEX ao_review_runs_auto_pull_request_sha_idx;
ALTER TABLE ao_review_runs DROP COLUMN trigger_source;
