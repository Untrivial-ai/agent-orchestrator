// Package gitlab observes GitLab merge requests for AO's SCM integrations.
//
// It implements the provider-neutral scm.Provider interface using the GitLab
// REST API v4. REST (rather than GraphQL) is a deliberate choice: it provides
// uniform access to CI pipeline jobs, merge-request approvals, and job trace
// logs (the raw build output used to diagnose failures) across GitLab.com and
// self-managed instances, and it avoids the version drift between GitLab's
// GraphQL schema and its REST surface. GraphQL remains a future option for
// bulk MR discovery if API consumption warrants it.
//
// # State mapping
//
// Each SCMObservation field is derived as follows:
//
//   - PR state: from merge_request.state + draft bool.
//     | GitLab state | draft | domain.PRState |
//     |--------------|-------|----------------|
//     | opened       | true  | draft          |
//     | opened       | false | open           |
//     | merged       | *     | merged         |
//     | closed       | *     | closed         |
//     | locked       | *     | closed         |
//
//   - CI: derived from the latest pipeline's status.
//     | Pipeline status        | domain.CIState |
//     |------------------------|----------------|
//     | success                | passing        |
//     | failed, canceled       | failing        |
//     | running, pending, ...  | pending        |
//     | skipped, manual, ""    | unknown        |
//
//   - Review: GitLab has no "changes_requested" concept. Approval is
//     derived from the merge_request/approvals endpoint's `approved` bool
//     (not len(approved_by), which can include approvals that don't satisfy
//     the applicable rules):
//     | Condition                         | domain.ReviewDecision   |
//     |-----------------------------------|-------------------------|
//     | approved == true                  | approved                |
//     | approved == false && required > 0  | review_required         |
//     | otherwise                         | none                    |
//
//   - Mergeability: from detailed_merge_status (preferred) or the deprecated
//     merge_status. Current detailed_merge_status values (mergeable, conflict,
//     checking, preparing, need_rebase, requested_changes, etc.) are mapped
//     directly; legacy values (can_be_merged, cannot_be_merged, unchecked) are
//     retained as aliases for older self-managed installations.
//
// # Authentication
//
// Tokens are resolved from AO_GITLAB_TOKEN, GITLAB_TOKEN, or by shelling out
// to `glab auth status --show-token`. The Authorization: Bearer header is used
// for REST requests because it works for both OAuth2 tokens and personal access
// tokens.
//
// # Host detection and self-managed support
//
// ParseRepository recognises gitlab.com and self-managed instances. A
// self-managed host derives its REST base as https://<host>/api/v4 so API
// traffic uses the correct instance rather than gitlab.com. Hosts containing
// "gitlab" in their name are also matched as a heuristic, with explicit
// rejection of GitHub host patterns to prevent collisions in the multi-provider
// dispatcher. Claim validation compares provider + host + full namespace so
// same-named repos on GitHub and GitLab (or on different GitLab instances)
// never pass validation.
//
// # Rate limiting
//
// A 429 response is parsed into a RateLimitError exposing Retry-After and
// RateLimit-Reset hints; the observer applies a per-provider cooldown with
// bounded backoff so it does not poll every 30s while rate-limited. The HTTP
// client uses a finite timeout so a hung endpoint cannot block the observer.
//
// # Incremental discovery
//
// ListPRsByRepo accepts an updatedAfter cursor and sends state=all +
// updated_after so only MRs updated since the last successful cursor are
// returned. The observer advances the cursor only after successful persistence,
// and subtracts an overlap window to absorb clock skew. First poll requests
// a full listing (updatedAfter = zero).
package gitlab
