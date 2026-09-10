-- Persist the per-session auto-review toggle and invalidate session subscribers
-- whenever it changes.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN auto_review_enabled INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS sessions_cdc_update;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
    OR OLD.auto_inject_review <> NEW.auto_inject_review
    OR OLD.auto_review_enabled <> NEW.auto_review_enabled
    OR OLD.harness <> NEW.harness
    OR OLD.runtime_launch_id <> NEW.runtime_launch_id
    OR OLD.agent_session_id <> NEW.agent_session_id
    OR OLD.native_transcript_path <> NEW.native_transcript_path
    OR OLD.auto_inject_ci <> NEW.auto_inject_ci
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END),
            'autoInjectCI', json(CASE WHEN NEW.auto_inject_ci THEN 'true' ELSE 'false' END),
            'autoReviewEnabled', json(CASE WHEN NEW.auto_review_enabled THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS sessions_cdc_update;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN auto_review_enabled;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
    OR OLD.auto_inject_review <> NEW.auto_inject_review
    OR OLD.harness <> NEW.harness
    OR OLD.runtime_launch_id <> NEW.runtime_launch_id
    OR OLD.agent_session_id <> NEW.agent_session_id
    OR OLD.native_transcript_path <> NEW.native_transcript_path
    OR OLD.auto_inject_ci <> NEW.auto_inject_ci
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END),
            'autoInjectCI', json(CASE WHEN NEW.auto_inject_ci THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;
-- +goose StatementEnd
