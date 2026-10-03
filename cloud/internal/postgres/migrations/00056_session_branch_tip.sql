-- +goose Up

-- session_branch_tip records the commit SHA the worker's checkpoint last put
-- forward as the session branch tip, riding the existing transcript capture
-- (the control plane has no other view of the worker's git state). Boot-time
-- adoption of an origin-only session branch requires this tip to be an
-- ancestor of origin/<branch>, so a force-pushed remote tip can never be
-- silently checked out. Empty means never captured; the worker's empty tip
-- never overwrites a recorded one (guarded in PutSessionTranscript).
ALTER TABLE ao_session_transcripts
    ADD COLUMN session_branch_tip TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE ao_session_transcripts
    DROP COLUMN IF EXISTS session_branch_tip;
