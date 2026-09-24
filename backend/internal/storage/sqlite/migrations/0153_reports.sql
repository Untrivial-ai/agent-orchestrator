-- +goose Up
-- +goose StatementBegin
CREATE TABLE reports (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT '' CHECK (state IN ('', 'checkpoint', 'needs_input', 'stuck', 'done')),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    message TEXT NOT NULL DEFAULT '' CHECK (length(message) <= 1000),
    created_at TIMESTAMP NOT NULL,
    delivery_state TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'claimed', 'acknowledged')),
    available_at TIMESTAMP NOT NULL,
    settlement_deadline TIMESTAMP,
    repeat_count INTEGER NOT NULL DEFAULT 1 CHECK (repeat_count >= 1),
    claim_token TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMP,
    delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
    acknowledged_at TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    CHECK (message = '' OR (state = '' AND note = '')),
    CHECK ((state = '' AND note = '') OR (state <> '' AND length(trim(note)) > 0)),
    CHECK ((state = 'done' AND settlement_deadline IS NOT NULL) OR (state <> 'done' AND settlement_deadline IS NULL)),
    CHECK (
        (delivery_state = 'pending' AND claim_token = '' AND claimed_at IS NULL AND acknowledged_at IS NULL)
        OR (delivery_state = 'claimed' AND claim_token <> '' AND claimed_at IS NOT NULL AND acknowledged_at IS NULL)
        OR (delivery_state = 'acknowledged' AND claim_token <> '' AND claimed_at IS NOT NULL AND acknowledged_at IS NOT NULL)
    )
);

CREATE TABLE report_outputs (
    report_id TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('artifact', 'pr_created', 'pr_reviewed')),
    reference TEXT NOT NULL CHECK (length(trim(reference)) > 0),
    label TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (report_id, position)
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
DROP TABLE IF EXISTS report_outputs;
DROP TABLE IF EXISTS reports;
-- +goose StatementEnd
