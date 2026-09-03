-- +goose Up
-- +goose StatementBegin
CREATE TABLE push_approvals (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    remote TEXT NOT NULL,
    remote_url TEXT NOT NULL,
    branch TEXT NOT NULL,
    expected_head_sha TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    approved_at DATETIME,
    approved_by TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    consumed_at DATETIME,
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'APPROVED', 'CONSUMED', 'EXPIRED', 'REVOKED', 'FAILED')),
    result TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_push_approvals_project_created
    ON push_approvals(project_id, created_at DESC);
CREATE INDEX idx_push_approvals_status_expiry
    ON push_approvals(status, expires_at);

CREATE TABLE git_action_audits (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('PUSH_REQUESTED', 'PUSH_APPROVED', 'PUSH_CANCELLED', 'PUSH_REJECTED', 'PUSH_STARTED', 'PUSH_SUCCEEDED', 'PUSH_FAILED')),
    repository TEXT NOT NULL,
    remote TEXT NOT NULL,
    branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    approval_id TEXT,
    requested_by TEXT NOT NULL,
    executed_by TEXT NOT NULL,
    started_at DATETIME NOT NULL,
    finished_at DATETIME,
    result TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (approval_id) REFERENCES push_approvals(id) ON DELETE SET NULL
);

CREATE INDEX idx_git_action_audits_project_started
    ON git_action_audits(project_id, started_at DESC);
CREATE INDEX idx_git_action_audits_approval
    ON git_action_audits(approval_id, started_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE git_action_audits;
DROP TABLE push_approvals;
-- +goose StatementEnd
