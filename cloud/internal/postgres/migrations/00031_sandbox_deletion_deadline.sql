-- +goose Up

-- Bound sandbox deletion. A provider that cannot reclaim a box (for example a
-- NodeOps VM stuck in "failed", which the CreateOS API refuses to destroy -- a
-- DELETE returns 200 but the box stays failed) otherwise leaves the reconciler
-- re-requesting deletion every tick forever, never converging to observed
-- 'deleted'. Record when deletion was first attempted so the reconciler can give
-- up past a deadline and release the row (quota) rather than loop indefinitely.
ALTER TABLE ao_sandboxes ADD COLUMN IF NOT EXISTS deletion_requested_at TIMESTAMPTZ;

-- +goose Down

ALTER TABLE ao_sandboxes DROP COLUMN IF EXISTS deletion_requested_at;
