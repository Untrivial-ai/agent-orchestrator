-- +goose Up
-- +goose StatementBegin
CREATE TABLE reports (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('free_form', 'pr_created', 'artifact', 'checkpoint', 'needs_input', 'stuck', 'done')),
    note TEXT NOT NULL CHECK (length(note) BETWEEN 1 AND 1000),
    created_at TIMESTAMP NOT NULL,
    delivery_state TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'claimed', 'acknowledged')),
    available_at TIMESTAMP NOT NULL,
    claim_token TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMP,
    delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
    acknowledged_at TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    CHECK (
        (delivery_state = 'pending' AND claim_token = '' AND claimed_at IS NULL AND acknowledged_at IS NULL)
        OR (delivery_state = 'claimed' AND claim_token <> '' AND claimed_at IS NOT NULL AND acknowledged_at IS NULL)
        OR (delivery_state = 'acknowledged' AND claim_token <> '' AND claimed_at IS NOT NULL AND acknowledged_at IS NOT NULL)
    )
);

CREATE INDEX idx_reports_delivery
    ON reports(delivery_state, available_at, created_at, id);
CREATE INDEX idx_reports_session
    ON reports(session_id, created_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_reports_session;
DROP INDEX IF EXISTS idx_reports_delivery;
DROP TABLE IF EXISTS reports;
-- +goose StatementEnd
