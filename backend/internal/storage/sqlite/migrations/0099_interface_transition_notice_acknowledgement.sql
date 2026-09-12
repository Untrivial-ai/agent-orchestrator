-- +goose Up
-- +goose StatementBegin
-- A terminal transition remains as durable diagnostics. This separate user fact
-- prevents its notice from looking new again after a client remount/restart or
-- when the same session is viewed from another client.
ALTER TABLE session_interface_transitions
    ADD COLUMN notice_acknowledged_at TIMESTAMP;
-- +goose StatementEnd

-- +goose StatementBegin
-- Acknowledgement is a cross-client invalidation just like a phase change.
-- Keep updated_at as the transition settlement time; the change-log event gets
-- the acknowledgement's own timestamp instead.
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_update;
CREATE TRIGGER session_interface_transitions_cdc_update
AFTER UPDATE ON session_interface_transitions
WHEN OLD.phase <> NEW.phase
    OR OLD.error_code <> NEW.error_code
    OR OLD.error_detail <> NEW.error_detail
    OR OLD.notice_acknowledged_at IS NOT NEW.notice_acknowledged_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.notice_acknowledged_at, NEW.updated_at)
    FROM sessions s WHERE s.id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_update;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE session_interface_transitions DROP COLUMN notice_acknowledged_at;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_interface_transitions_cdc_update
AFTER UPDATE ON session_interface_transitions
WHEN OLD.phase <> NEW.phase
    OR OLD.error_code <> NEW.error_code
    OR OLD.error_detail <> NEW.error_detail
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM sessions s WHERE s.id = NEW.session_id;
END;
-- +goose StatementEnd
