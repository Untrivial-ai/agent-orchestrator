-- +goose Up

-- Allow more than one QUEUED turn per session so concurrent fan-in (e.g. several
-- orchestrator workers each calling `ao report` at once) enqueues a FIFO queue
-- instead of the second-and-later inserts failing with a unique-violation (which
-- surfaced to the worker as a 409 "conflict"). The single-executing-turn
-- guarantee is preserved: at most one turn may be provisioning/running/
-- cancel_requested at a time. ClaimWorkerTurn already drains a multi-row queue
-- (ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1), and the frontend already
-- renders multiple queued turns (QueuedMessageDock), so this aligns cloud
-- behaviour with the existing chat queue.
DROP INDEX IF EXISTS ao_turns_one_active_per_session;

CREATE UNIQUE INDEX ao_turns_one_active_per_session
    ON ao_turns(session_id)
    WHERE state IN ('provisioning', 'running', 'cancel_requested');

-- +goose Down

DROP INDEX IF EXISTS ao_turns_one_active_per_session;

CREATE UNIQUE INDEX ao_turns_one_active_per_session
    ON ao_turns(session_id)
    WHERE state IN ('queued', 'provisioning', 'running', 'cancel_requested');
