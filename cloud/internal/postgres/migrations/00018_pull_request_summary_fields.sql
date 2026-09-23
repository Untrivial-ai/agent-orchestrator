-- +goose Up

-- These four columns close the gap between ao_pull_requests and the public
-- agent-orchestrator repo's contract.PullRequestSummary (see
-- contracts/cloud/openapi.yaml's PullRequestSummary schema), which the
-- shared product-ui review components already expect: author and diff-size
-- are required fields on every pull request card the local desktop app
-- renders, and AO Cloud's own web UI reuses those same components.
ALTER TABLE ao_pull_requests
    ADD COLUMN author TEXT NOT NULL DEFAULT '',
    ADD COLUMN additions INTEGER NOT NULL DEFAULT 0 CHECK (additions >= 0),
    ADD COLUMN deletions INTEGER NOT NULL DEFAULT 0 CHECK (deletions >= 0),
    ADD COLUMN changed_files INTEGER NOT NULL DEFAULT 0 CHECK (changed_files >= 0);

-- +goose Down
ALTER TABLE ao_pull_requests
    DROP COLUMN author,
    DROP COLUMN additions,
    DROP COLUMN deletions,
    DROP COLUMN changed_files;
