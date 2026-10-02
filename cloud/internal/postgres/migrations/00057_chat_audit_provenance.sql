-- +goose Up

-- Recover legacy worker-message origin by tenant, session and event sequence.
CREATE INDEX ao_audit_events_chat_provenance_idx
    ON ao_audit_events (org_id, resource_id, ((metadata->>'sequence')::bigint))
    WHERE resource_type = 'session'
      AND action = 'session.message_queued'
      AND metadata ? 'actorSessionId';

-- +goose Down

DROP INDEX ao_audit_events_chat_provenance_idx;
