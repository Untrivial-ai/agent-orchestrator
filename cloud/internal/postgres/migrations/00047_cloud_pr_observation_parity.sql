-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN IF NOT EXISTS auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS terminate_on_pr_merge BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE ao_pull_requests
    ADD COLUMN IF NOT EXISTS author_avatar_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS base_sha TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS merge_commit_sha TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_at_provider TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at_provider TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS merged_at_provider TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS closed_at_provider TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS review_partial BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE ao_pr_review_threads
    ADD COLUMN IF NOT EXISTS is_bot BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE ao_pr_reviews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL,
    provider_review_id TEXT NOT NULL,
    provider_database_id BIGINT,
    author_login TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'none'
        CHECK (state IN ('none', 'approved', 'changes_requested', 'review_required')),
    body TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    target_sha TEXT NOT NULL DEFAULT '',
    is_bot BOOLEAN NOT NULL DEFAULT false,
    auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE,
    submitted_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pull_request_id, provider_review_id),
    CONSTRAINT ao_pr_reviews_pr_fk FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id) ON DELETE CASCADE
);

CREATE TABLE ao_pr_review_comments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    pull_request_id UUID NOT NULL,
    provider_comment_id TEXT NOT NULL,
    provider_database_id BIGINT,
    provider_thread_id TEXT NOT NULL,
    provider_review_id TEXT NOT NULL DEFAULT '',
    author_login TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    line INTEGER CHECK (line IS NULL OR line > 0),
    is_resolved BOOLEAN NOT NULL DEFAULT false,
    is_outdated BOOLEAN NOT NULL DEFAULT false,
    is_bot BOOLEAN NOT NULL DEFAULT false,
    auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pull_request_id, provider_comment_id),
    CONSTRAINT ao_pr_review_comments_pr_fk FOREIGN KEY (org_id, pull_request_id)
        REFERENCES ao_pull_requests(org_id, id) ON DELETE CASCADE
);

CREATE INDEX ao_pr_reviews_lookup_idx ON ao_pr_reviews(pull_request_id, submitted_at DESC);
CREATE INDEX ao_pr_review_comments_lookup_idx ON ao_pr_review_comments(pull_request_id, is_resolved, updated_at DESC);

ALTER TABLE ao_pr_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_pr_reviews FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_pr_reviews_tenant_policy ON ao_pr_reviews
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());

ALTER TABLE ao_pr_review_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE ao_pr_review_comments FORCE ROW LEVEL SECURITY;
CREATE POLICY ao_pr_review_comments_tenant_policy ON ao_pr_review_comments
    USING (org_id = ao_current_org_id()) WITH CHECK (org_id = ao_current_org_id());

-- +goose Down
DROP POLICY IF EXISTS ao_pr_review_comments_tenant_policy ON ao_pr_review_comments;
DROP POLICY IF EXISTS ao_pr_reviews_tenant_policy ON ao_pr_reviews;
DROP INDEX IF EXISTS ao_pr_review_comments_lookup_idx;
DROP INDEX IF EXISTS ao_pr_reviews_lookup_idx;
DROP TABLE IF EXISTS ao_pr_review_comments;
DROP TABLE IF EXISTS ao_pr_reviews;
ALTER TABLE ao_pr_review_threads
    DROP COLUMN IF EXISTS auto_inject_review,
    DROP COLUMN IF EXISTS is_bot;
ALTER TABLE ao_pull_requests
    DROP COLUMN IF EXISTS review_partial,
    DROP COLUMN IF EXISTS closed_at_provider,
    DROP COLUMN IF EXISTS merged_at_provider,
    DROP COLUMN IF EXISTS updated_at_provider,
    DROP COLUMN IF EXISTS created_at_provider,
    DROP COLUMN IF EXISTS merge_commit_sha,
    DROP COLUMN IF EXISTS base_sha,
    DROP COLUMN IF EXISTS author_avatar_url;
ALTER TABLE ao_sessions
    DROP COLUMN IF EXISTS terminate_on_pr_merge,
    DROP COLUMN IF EXISTS auto_inject_review;
