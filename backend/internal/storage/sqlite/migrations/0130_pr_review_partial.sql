-- +goose Up
-- Records whether the latest review-thread observation was complete. A partial
-- fetch (provider thread-window cap) means stored thread rows cannot support an
-- exact unresolved-thread count: rows outside the window are missing, and rows
-- resolved outside the window are preserved stale by the merge write mode.
-- Existing rows default to partial=TRUE: pre-column observations carry no
-- completeness signal, so they must stay uncertain until the next successful
-- full review fetch flips the flag (the observer treats a completeness change
-- as a review change so the flip persists even when content hashes match).
ALTER TABLE pr ADD COLUMN review_partial BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
