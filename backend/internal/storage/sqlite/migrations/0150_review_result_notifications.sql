-- +goose Up
-- +goose StatementBegin
-- SQLite cannot widen a CHECK constraint in place. Rebuild the table to add
-- terminal review-result kinds and a durable per-review-run dedupe key.
CREATE TABLE notifications_next (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN (
        'needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged',
        'review_completed', 'review_changes_requested'
    )),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP,
    source_key TEXT NOT NULL DEFAULT ''
);

INSERT INTO notifications_next (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at
)
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_next RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);
CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);
CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);
CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC) WHERE dismissed_at IS NULL;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE source_key = '' AND (status = 'unread' OR resolved_at IS NULL);
CREATE UNIQUE INDEX idx_notifications_source_dedupe
    ON notifications(source_key) WHERE source_key <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM notifications
WHERE type IN ('review_completed', 'review_changes_requested');

CREATE TABLE notifications_previous (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN (
        'needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged'
    )),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP
);

INSERT INTO notifications_previous
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_previous RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);
CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);
CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);
CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC) WHERE dismissed_at IS NULL;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE status = 'unread' OR resolved_at IS NULL;
-- +goose StatementEnd
