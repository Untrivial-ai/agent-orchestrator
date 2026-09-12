-- +goose Up

-- A visible workspace terminal is interactive activity even when the user is
-- only reading output. The short lease bridges sandbox resume and is renewed
-- while the browser's workspace WebSocket remains open.
ALTER TABLE ao_sandboxes ADD COLUMN interactive_until TIMESTAMPTZ;

-- +goose Down
ALTER TABLE ao_sandboxes DROP COLUMN interactive_until;
