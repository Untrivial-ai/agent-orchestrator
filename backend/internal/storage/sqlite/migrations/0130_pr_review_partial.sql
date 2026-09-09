-- +goose Up
-- Records whether the latest review-thread observation was complete. A partial
-- fetch (provider thread-window cap) means stored thread rows cannot support an
-- exact unresolved-thread count: rows outside the window are missing, and rows
-- resolved outside the window are preserved stale by the merge write mode.
ALTER TABLE pr ADD COLUMN review_partial BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
