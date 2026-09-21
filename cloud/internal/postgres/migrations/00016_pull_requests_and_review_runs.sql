-- +goose Up

-- ao_pull_requests already exists as of the founding schema (00001); this
-- migration only extends it with ao_review_state, which mirrors the public
-- agent-orchestrator repo's contract.AOReviewState: whether this PR's
-- current head has an AO review pass that's up to date, still needed, in
-- flight, or ineligible. It is a summary derived from ao_review_runs, kept
-- here (rather than computed on every read) so a session's PR list is a
-- single-table read.
ALTER TABLE ao_pull_requests
    ADD COLUMN ao_review_state TEXT NOT NULL DEFAULT 'needs_review'
        CHECK (ao_review_state IN (
            'needs_review', 'running', 'up_to_date', 'changes_requested', 'ineligible'
        ));

-- ao_review_runs is one AO-triggered automated review pass against a specific
-- commit of a pull request. The (pull_request_id, target_sha) uniqueness is
-- the fencing: at most one review run exists per commit, so a review can
-- never be triggered twice against the same SHA, and a new push always gets
-- its own fresh run rather than reusing a stale verdict.
CREATE TABLE ao_review_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL,
    -- review_session_id is the sandboxed session doing the review work itself
    -- (a fresh checkout of the PR's head, running a review-focused harness
    -- turn) — distinct from the pull request's own originating session.
    review_session_id UUID,
    target_sha TEXT NOT NULL CHECK (btrim(target_sha) <> ''),
    status TEXT NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'complete', 'delivered', 'failed', 'cancelled')),
    verdict TEXT NOT NULL DEFAULT ''
        CHECK (verdict IN ('', 'approved', 'changes_requested')),
    body TEXT NOT NULL DEFAULT '',
    provider_review_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    UNIQUE (pull_request_id, target_sha),
    CONSTRAINT ao_review_runs_pull_request_fk
        FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id)
        ON DELETE CASCADE
);

CREATE INDEX ao_review_runs_pull_request_idx ON ao_review_runs(pull_request_id);

-- +goose StatementBegin
DO $$
BEGIN
    ALTER TABLE ao_review_runs ENABLE ROW LEVEL SECURITY;
    ALTER TABLE ao_review_runs FORCE ROW LEVEL SECURITY;
    CREATE POLICY ao_review_runs_tenant_policy ON ao_review_runs
        USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE ao_review_runs;
ALTER TABLE ao_pull_requests DROP COLUMN ao_review_state;
