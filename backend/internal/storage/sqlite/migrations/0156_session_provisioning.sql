-- +goose Up
-- A Chat spawn now answers the API before its worktree and controller exist, so
-- the session row is visible while it is still being built. These columns are
-- what the board and the chat surface read to tell "still starting" apart from
-- "running", and to explain a start that failed instead of deleting the row the
-- user is already looking at.
ALTER TABLE sessions ADD COLUMN provision_state TEXT NOT NULL DEFAULT 'ready'
    CHECK (provision_state IN ('provisioning', 'ready', 'failed'));
ALTER TABLE sessions ADD COLUMN provision_error TEXT NOT NULL DEFAULT '';

DROP TRIGGER IF EXISTS sessions_cdc_update;

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
    OR OLD.latest_user_prompt_at IS NOT NEW.latest_user_prompt_at
    OR OLD.provision_state <> NEW.provision_state
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
            'autoReviewEnabled', json(CASE WHEN NEW.auto_review_enabled THEN 'true' ELSE 'false' END),
            'provisionState', NEW.provision_state
        ),
        NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS sessions_cdc_update;

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
    OR OLD.latest_user_prompt_at IS NOT NEW.latest_user_prompt_at
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

ALTER TABLE sessions DROP COLUMN provision_error;
ALTER TABLE sessions DROP COLUMN provision_state;
