-- +goose Up

-- review_terminal_id identifies the dedicated agent process AO-triggered
-- review runs in — a second, independent terminal in the PR-raising
-- session's own sandbox, not a message appended to that session's ongoing
-- conversation. See OpenReviewTerminal/CloseReviewTerminal in
-- internal/postgres/review_run_store.go.
ALTER TABLE ao_review_runs ADD COLUMN review_terminal_id UUID;

-- +goose Down
ALTER TABLE ao_review_runs DROP COLUMN review_terminal_id;
