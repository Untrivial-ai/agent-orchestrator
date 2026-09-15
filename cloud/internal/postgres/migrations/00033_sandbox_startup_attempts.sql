-- Repairing a never-checked-in worker must grant each fresh install a full
-- startup window: without resetting the clock, every reconcile tick after the
-- first missed deadline re-triggered the repair, killing each new worker
-- mid-boot forever (a 2-second window can never fit a repository clone). The
-- attempt counter is the new bound: a bounded number of full windows, then the
-- terminal-startup parking path stops compute and billing.
-- +goose Up
ALTER TABLE ao_sandboxes ADD COLUMN startup_attempts INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ao_sandboxes DROP COLUMN startup_attempts;
