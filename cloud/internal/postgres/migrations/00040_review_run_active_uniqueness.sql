-- +goose Up

-- A terminal review is historical data, not an idempotency fence. Keep only
-- running reviews unique so cancellation and startup failures can be retried
-- for the same pull-request commit.
ALTER TABLE ao_review_runs
    DROP CONSTRAINT IF EXISTS ao_review_runs_pull_request_id_target_sha_key;

CREATE UNIQUE INDEX IF NOT EXISTS ao_review_runs_active_pull_request_sha_idx
    ON ao_review_runs (pull_request_id, target_sha)
    WHERE status = 'running';

-- +goose Down
DROP INDEX ao_review_runs_active_pull_request_sha_idx;
ALTER TABLE ao_review_runs
    ADD CONSTRAINT ao_review_runs_pull_request_id_target_sha_key
    UNIQUE (pull_request_id, target_sha);
