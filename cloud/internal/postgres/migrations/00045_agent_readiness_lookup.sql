-- +goose Up
CREATE INDEX ao_events_agent_ready_idx
    ON ao_events (org_id, session_id, (payload->>'epoch'), (payload->>'workerId'))
    WHERE type = 'agent.ready';

-- +goose Down
DROP INDEX ao_events_agent_ready_idx;
