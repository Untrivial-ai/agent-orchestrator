-- Persist the first-class AO review request identity and model selection. The
-- immutable PR head already lives in target_sha; requester/model complete the
-- request facts exposed back to the worker.

-- +goose Up
ALTER TABLE review ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE review_run ADD COLUMN requested_by_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE review_run ADD COLUMN model TEXT NOT NULL DEFAULT '';

-- A different model is an explicit second opinion on the same head. Preserve
-- the existing duplicate/concurrent trigger backstop within one model.
-- +goose StatementBegin
DROP INDEX idx_review_run_session_pr_sha_harness;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX idx_review_run_session_pr_sha_harness_model
    ON review_run (session_id, pr_url, target_sha, harness, model)
    WHERE target_sha != ''
        AND status NOT IN ('failed', 'cancelled')
        AND (status = 'running' OR verdict NOT IN ('', 'changes_requested'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_review_run_session_pr_sha_harness_model;
-- +goose StatementEnd

-- Collapse model-specific second opinions before restoring the historical key.
-- Prefer a successful terminal result, then the newest row.
-- +goose StatementBegin
DELETE FROM review_run
WHERE target_sha != ''
  AND status NOT IN ('failed', 'cancelled')
  AND (status = 'running' OR verdict NOT IN ('', 'changes_requested'))
  AND rowid NOT IN (
    SELECT rowid FROM (
      SELECT rowid,
             ROW_NUMBER() OVER (
               PARTITION BY session_id, pr_url, target_sha, harness
               ORDER BY CASE status WHEN 'complete' THEN 0 WHEN 'delivered' THEN 0 WHEN 'running' THEN 1 ELSE 2 END,
                        created_at DESC,
                        rowid DESC
             ) AS rn
      FROM review_run
      WHERE target_sha != ''
        AND status NOT IN ('failed', 'cancelled')
        AND (status = 'running' OR verdict NOT IN ('', 'changes_requested'))
    )
    WHERE rn = 1
  );
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_review_run_session_pr_sha_harness
    ON review_run (session_id, pr_url, target_sha, harness)
    WHERE target_sha != ''
        AND status NOT IN ('failed', 'cancelled')
        AND (status = 'running' OR verdict NOT IN ('', 'changes_requested'));
-- +goose StatementEnd

ALTER TABLE review_run DROP COLUMN model;
ALTER TABLE review_run DROP COLUMN requested_by_session_id;
ALTER TABLE review DROP COLUMN model;
