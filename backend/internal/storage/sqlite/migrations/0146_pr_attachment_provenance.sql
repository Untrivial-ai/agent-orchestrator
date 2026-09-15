-- +goose Up

ALTER TABLE pr ADD COLUMN attachment_source TEXT NOT NULL DEFAULT 'legacy'
    CHECK (attachment_source IN ('legacy', 'automatic', 'explicit'));

-- PR removal changes the session read model. Reuse session_updated as the
-- invalidation signal, following the existing conversation CDC pattern, rather
-- than rebuilding change_log solely to add a pr_deleted event type. Keep the
-- event project-scoped (session_id NULL): PR rows also cascade when their
-- session is deleted, and a fresh change_log FK back to that session would make
-- the parent deletion fail. The payload still identifies the affected session.
-- +goose StatementBegin
CREATE TRIGGER pr_cdc_delete
BEFORE DELETE ON pr
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT sessions.project_id, NULL, 'session_updated',
        json_object('id', sessions.id, 'sessionId', sessions.id, 'pr', OLD.url,
                    'activity', sessions.activity_state,
                    'isTerminated', json(CASE WHEN sessions.is_terminated THEN 'true' ELSE 'false' END)),
        CURRENT_TIMESTAMP
    FROM sessions
    WHERE sessions.id = OLD.session_id;
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS pr_cdc_delete;
ALTER TABLE pr DROP COLUMN attachment_source;
