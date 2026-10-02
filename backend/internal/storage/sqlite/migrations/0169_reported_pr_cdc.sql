-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER report_outputs_pr_created_cdc
AFTER INSERT ON report_outputs
WHEN NEW.kind = 'pr_created'
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT r.project_id, r.session_id, 'session_updated', json_object('id', r.session_id), r.created_at
    FROM reports r WHERE r.id = NEW.report_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS report_outputs_pr_created_cdc;
-- +goose StatementEnd
