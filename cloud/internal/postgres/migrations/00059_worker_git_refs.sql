-- +goose Up
CREATE TABLE ao_worker_git_refs (
    org_id UUID NOT NULL,
    session_id UUID NOT NULL,
    github_repository_id BIGINT NOT NULL,
    branch TEXT NOT NULL CHECK (btrim(branch) <> ''),
    head_sha TEXT NOT NULL CHECK (btrim(head_sha) <> ''),
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, branch),
    FOREIGN KEY (org_id, session_id) REFERENCES ao_sessions(org_id, id) ON DELETE CASCADE
);
CREATE INDEX ao_worker_git_refs_head_idx
    ON ao_worker_git_refs (org_id, github_repository_id, branch, head_sha);
ALTER TABLE ao_worker_git_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_worker_git_refs FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_worker_git_refs_tenant_policy ON ao_worker_git_refs
    USING (org_id = ao_current_org_id())
    WITH CHECK (org_id = ao_current_org_id());

-- +goose Down
DROP TABLE ao_worker_git_refs;
