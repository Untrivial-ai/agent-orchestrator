-- name: UpsertPR :exec
INSERT INTO pr (
    url, session_id, number, pr_state, review_decision, ci_state, mergeability, updated_at, state_changed_at,
    provider, host, repo, provider_id, source_branch, target_branch, head_sha, title,
    additions, deletions, changed_files, author, base_sha, merge_commit_sha,
    is_draft, is_merged, is_closed,
    provider_state, provider_mergeable, provider_merge_state_status, html_url,
    created_at_provider, updated_at_provider, merged_at_provider, closed_at_provider,
    metadata_hash, ci_hash, review_hash, observed_at, ci_observed_at, review_observed_at, auto_inject_ci
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
    COALESCE((SELECT auto_inject_ci FROM sessions WHERE id = ?), TRUE))
ON CONFLICT (url) DO UPDATE SET
    number = excluded.number,
    state_changed_at = CASE
        WHEN pr.pr_state != excluded.pr_state THEN
            CASE
                WHEN excluded.pr_state = 'merged' THEN COALESCE(excluded.merged_at_provider, excluded.updated_at_provider, excluded.observed_at, excluded.updated_at)
                WHEN excluded.pr_state = 'closed' THEN COALESCE(excluded.closed_at_provider, excluded.updated_at_provider, excluded.observed_at, excluded.updated_at)
                ELSE COALESCE(excluded.updated_at_provider, excluded.observed_at, excluded.updated_at)
            END
        WHEN pr.state_changed_at IS NULL THEN excluded.state_changed_at
        ELSE pr.state_changed_at
    END,
    pr_state = excluded.pr_state,
    review_decision = excluded.review_decision,
    ci_state = excluded.ci_state,
    mergeability = excluded.mergeability,
    updated_at = excluded.updated_at,
    provider = excluded.provider,
    host = excluded.host,
    repo = excluded.repo,
    provider_id = CASE WHEN excluded.provider_id != '' THEN excluded.provider_id ELSE pr.provider_id END,
    source_branch = excluded.source_branch,
    target_branch = excluded.target_branch,
    head_sha = excluded.head_sha,
    title = excluded.title,
    additions = excluded.additions,
    deletions = excluded.deletions,
    changed_files = excluded.changed_files,
    author = excluded.author,
    base_sha = excluded.base_sha,
    merge_commit_sha = excluded.merge_commit_sha,
    is_draft = excluded.is_draft,
    is_merged = excluded.is_merged,
    is_closed = excluded.is_closed,
    provider_state = excluded.provider_state,
    provider_mergeable = excluded.provider_mergeable,
    provider_merge_state_status = excluded.provider_merge_state_status,
    html_url = excluded.html_url,
    created_at_provider = excluded.created_at_provider,
    updated_at_provider = excluded.updated_at_provider,
    merged_at_provider = excluded.merged_at_provider,
    closed_at_provider = excluded.closed_at_provider,
    metadata_hash = excluded.metadata_hash,
    ci_hash = excluded.ci_hash,
    review_hash = excluded.review_hash,
    observed_at = excluded.observed_at,
    ci_observed_at = excluded.ci_observed_at,
    review_observed_at = excluded.review_observed_at;

-- name: UpsertLegacyPR :exec
INSERT INTO pr (
    url, session_id, number, pr_state, review_decision, ci_state, mergeability, updated_at, state_changed_at,
    is_draft, is_merged, is_closed, auto_inject_ci
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
    COALESCE((SELECT auto_inject_ci FROM sessions WHERE id = ?), TRUE))
ON CONFLICT (url) DO UPDATE SET
    number = excluded.number,
    state_changed_at = CASE
        WHEN pr.pr_state != excluded.pr_state THEN excluded.updated_at
        WHEN pr.state_changed_at IS NULL THEN excluded.state_changed_at
        ELSE pr.state_changed_at
    END,
    pr_state = excluded.pr_state,
    review_decision = excluded.review_decision,
    ci_state = excluded.ci_state,
    mergeability = excluded.mergeability,
    updated_at = excluded.updated_at,
    is_draft = excluded.is_draft,
    is_merged = excluded.is_merged,
    is_closed = excluded.is_closed;

-- name: GetPR :one
SELECT * FROM pr WHERE url = ?;

-- name: GetPRByURLOrAlias :one
SELECT pr.*
FROM pr
WHERE pr.url = COALESCE(
    (SELECT canonical_url FROM pr_url_alias WHERE alias_url = sqlc.arg(url)),
    sqlc.arg(url)
);

-- name: GetPRByProviderIdentity :one
SELECT *
FROM pr
WHERE provider = sqlc.arg(provider)
  AND host = sqlc.arg(host)
  AND provider_id = sqlc.arg(provider_id)
  AND provider_id != '';

-- name: ClearPRProviderIdentity :exec
UPDATE pr SET provider_id = '' WHERE url = ?;

-- name: DeletePRByURL :exec
DELETE FROM pr WHERE url = ?;

-- name: DeletePRAlias :exec
DELETE FROM pr_url_alias WHERE alias_url = ?;

-- name: RepointPRAliases :exec
UPDATE pr_url_alias
SET canonical_url = sqlc.arg(canonical_url)
WHERE canonical_url = sqlc.arg(previous_url);

-- name: UpsertPRAlias :exec
INSERT INTO pr_url_alias(alias_url, canonical_url)
VALUES (sqlc.arg(alias_url), sqlc.arg(canonical_url))
ON CONFLICT(alias_url) DO UPDATE SET canonical_url = excluded.canonical_url;

-- name: MovePRAliasChecks :exec
UPDATE OR IGNORE pr_checks SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: DeletePRAliasChecks :exec
DELETE FROM pr_checks WHERE pr_url = ?;

-- name: MovePRAliasReviews :exec
UPDATE OR IGNORE pr_reviews SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: DeletePRAliasReviews :exec
DELETE FROM pr_reviews WHERE pr_url = ?;

-- name: MovePRAliasReviewThreads :exec
UPDATE OR IGNORE pr_review_threads SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: DeletePRAliasReviewThreads :exec
DELETE FROM pr_review_threads WHERE pr_url = ?;

-- name: MovePRAliasComments :exec
UPDATE OR IGNORE pr_comment SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: DeletePRAliasComments :exec
DELETE FROM pr_comment WHERE pr_url = ?;

-- Preserve both notification records when their open-dedupe keys collide:
-- the canonical notification stays open and the older alias notification
-- becomes resolved history before its URL is updated.
-- name: ResolveConflictingPRAliasNotifications :exec
UPDATE notifications
SET status = 'read', resolved_at = COALESCE(notifications.resolved_at, notifications.created_at)
WHERE notifications.pr_url = sqlc.arg(previous_url)
  AND (notifications.status = 'unread' OR notifications.resolved_at IS NULL)
  AND EXISTS (
      SELECT 1 FROM notifications AS current
      WHERE current.pr_url = sqlc.arg(canonical_url)
        AND current.session_id = notifications.session_id
        AND current.type = notifications.type
        AND (current.status = 'unread' OR current.resolved_at IS NULL)
  );

-- name: MovePRAliasNotifications :exec
UPDATE notifications SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: MovePRAliasReviewState :exec
UPDATE review SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- name: MovePRAliasReviewRuns :exec
UPDATE OR IGNORE review_run SET pr_url = sqlc.arg(canonical_url) WHERE pr_url = sqlc.arg(previous_url);

-- Rows left on the previous URL collided with the canonical review-run
-- idempotency key and therefore represent the same logical review pass.
-- name: DeletePRAliasReviewRuns :exec
DELETE FROM review_run WHERE pr_url = ?;

-- name: ListPRsBySession :many
SELECT * FROM pr
WHERE session_id = ?
ORDER BY updated_at DESC;

-- name: GetPRLastNudgeSignature :one
SELECT last_nudge_signature FROM pr WHERE url = ?;

-- name: UpdatePRLastNudgeSignature :exec
UPDATE pr SET last_nudge_signature = ? WHERE url = ?;

-- name: SetPRAutoInjectCIBySession :exec
UPDATE pr SET auto_inject_ci = ? WHERE session_id = ?;

-- name: GetDisplayPRFactsBySession :one
SELECT
    pr.url,
    pr.number,
    pr.pr_state,
    pr.review_decision,
    pr.ci_state,
    pr.mergeability,
    pr.head_sha,
    pr.updated_at,
    EXISTS (
        SELECT 1
        FROM pr_comment
        WHERE pr_comment.pr_url = pr.url
          AND pr_comment.resolved = 0
          AND pr_comment.is_bot = 0
    ) AS review_comments
FROM pr
WHERE pr.session_id = ?
ORDER BY
    CASE WHEN pr.pr_state NOT IN ('merged', 'closed') THEN 0 ELSE 1 END,
    pr.updated_at DESC
LIMIT 1;

-- name: ListPRFactsBySession :many
-- All PR snapshots for a session (every state), with source/target branch for
-- stack derivation and the unresolved-comment flag. The status aggregator
-- filters open vs merged/closed in Go and derives stacks from the branches.
SELECT
    pr.url,
    pr.number,
    pr.pr_state,
    pr.review_decision,
    pr.ci_state,
    pr.mergeability,
    pr.source_branch,
    pr.target_branch,
    pr.head_sha,
    pr.updated_at,
    EXISTS (
        SELECT 1
        FROM pr_comment
        WHERE pr_comment.pr_url = pr.url
          AND pr_comment.resolved = 0
          AND pr_comment.is_bot = 0
    ) AS review_comments
FROM pr
WHERE pr.session_id = ?
ORDER BY pr.updated_at DESC;

-- name: ClaimPRForSession :exec
INSERT INTO pr (url, session_id, number, pr_state, review_decision, ci_state, mergeability, updated_at, state_changed_at, auto_inject_ci)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
    COALESCE((SELECT auto_inject_ci FROM sessions WHERE id = ?), TRUE))
ON CONFLICT (url) DO UPDATE SET
    session_id = excluded.session_id,
    state_changed_at = CASE
        WHEN pr.pr_state != excluded.pr_state THEN excluded.updated_at
        WHEN pr.state_changed_at IS NULL THEN excluded.state_changed_at
        ELSE pr.state_changed_at
    END,
    review_decision = excluded.review_decision,
    updated_at = excluded.updated_at;

-- name: GetPRClaimAndOwner :one
-- Returns the current owner of a PR URL plus whether that owner is
-- terminated. Used by the takeover guard inside the claim tx.
SELECT pr.session_id, sessions.is_terminated
FROM pr
JOIN sessions ON sessions.id = pr.session_id
WHERE pr.url = ?;
