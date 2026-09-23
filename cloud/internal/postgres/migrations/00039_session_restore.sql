-- +goose Up

-- ao_session_transcripts durably captures what a deleted session's sandbox
-- would otherwise lose: the harness transcript JSONL that `--resume` reads and
-- a preserved git ref the worker can re-fetch. The worker periodically pushes
-- the latest capture; RESTORE re-provisions a fresh sandbox under the same
-- session_id and rehydrates from the most recent row. One row per session
-- (the newest capture wins), so an UPSERT on the primary key is the write path.
CREATE TABLE ao_session_transcripts (
    org_id UUID NOT NULL,
    session_id UUID NOT NULL,
    agent_session_id TEXT NOT NULL DEFAULT '',
    harness TEXT NOT NULL DEFAULT '',
    transcript BYTEA NOT NULL,
    preserved_git_ref TEXT NOT NULL DEFAULT '',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id),
    CONSTRAINT ao_session_transcripts_session_fk
        FOREIGN KEY (org_id, session_id)
        REFERENCES ao_sessions(org_id, id)
        ON DELETE CASCADE
);

ALTER TABLE ao_session_transcripts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_session_transcripts FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_session_transcripts_tenant_policy ON ao_session_transcripts
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());
CREATE POLICY ao_session_transcripts_service_policy ON ao_session_transcripts
    USING (ao_service_context())
    WITH CHECK (ao_service_context());

-- +goose Down

DROP TABLE IF EXISTS ao_session_transcripts;
