-- +goose Up
-- +goose StatementBegin

-- Add agent-turn and CI failure notification kinds. event_key lets recurring
-- terminal events (one per turn) coexist without weakening deduplication for
-- long-lived actionable notifications or one-off PR outcomes.
CREATE TABLE notifications_new (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    event_key TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (
        type IN (
            'turn_completed',
            'turn_failed',
            'needs_input',
            'ci_failed',
            'ready_to_merge',
            'pr_merged',
            'pr_closed_unmerged'
        )
    ),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP
);

INSERT INTO notifications_new (
    id, session_id, project_id, pr_url, event_key, type, title, body, status, created_at, resolved_at, dismissed_at
)
SELECT id, session_id, project_id, pr_url, '', type, title, body, status, created_at, resolved_at, dismissed_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);

CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);

CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url, event_key)
    WHERE status = 'unread' OR resolved_at IS NULL;

CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);

CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC)
    WHERE dismissed_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

CREATE TABLE notifications_old (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (
        type IN (
            'needs_input',
            'ready_to_merge',
            'pr_merged',
            'pr_closed_unmerged'
        )
    ),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP
);

INSERT INTO notifications_old (
    id, session_id, project_id, pr_url, type, title, body, status, created_at, resolved_at, dismissed_at
)
SELECT id, session_id, project_id, pr_url, type, title, body, status, created_at, resolved_at, dismissed_at
FROM notifications
WHERE type IN ('needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged');

DROP TABLE notifications;
ALTER TABLE notifications_old RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);

CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);

CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE status = 'unread' OR resolved_at IS NULL;

CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);

CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC)
    WHERE dismissed_at IS NULL;

-- +goose StatementEnd
