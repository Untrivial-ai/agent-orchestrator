// Package onedev observes OneDev pull requests for AO's SCM integrations.
//
// The package satisfies the observer's scm.Provider contract: repository-URL
// parsing, the pull-request list and its guard, the commit-checks guard,
// batched pull-request fetches, failed-build log tails, and review threads.
// The daemon registers it alongside the GitHub and GitLab providers in
// internal/daemon/scm_wiring.go.
//
// # Always self-hosted
//
// Unlike GitHub and GitLab there is no public OneDev SaaS instance — every
// deployment is self-hosted. There is therefore no default host and no
// default API base: the host allowlist (AO_ONEDEV_ALLOWED_HOSTS) is required
// configuration, and NewProvider fails with ErrNoAllowedHosts when it is
// empty rather than silently defaulting to some public endpoint.
//
// Allowlist entries may carry an explicit scheme because self-hosted OneDev
// is commonly reached over plain HTTP on a private network:
//
//	onedev.example.com           -> https://onedev.example.com/~api
//	onedev.example.com:6610      -> https://onedev.example.com:6610/~api
//	http://10.0.0.30:6610        -> http://10.0.0.30:6610/~api
//
// # API surface
//
// OneDev's REST root is "/~api", not "/api". Authentication is either a
// bearer access token or HTTP basic auth; both are modelled by Credential.
// GET /~api/projects is the preflight target — it is cheap, requires
// authentication, and returns 401 for a bad or missing credential.
//
// Listing endpoints paginate with offset/count query parameters. There is no
// Link header, and count is capped at 100 (a larger value is rejected with
// HTTP 406), so doGETPaginated walks pages until one comes back short.
//
// Three shapes of OneDev's query language are easy to get wrong and are
// pinned by tests:
//
//   - The date operator is "since", not "after". `"Last Activity Date" is
//     after "..."` is rejected with HTTP 406 "Invalid query".
//   - A pull-request reference in a build query must be project-qualified —
//     "productone#106". A bare number is rejected with "Reference project not
//     specified" even when the query already names the project.
//   - Ids in paths are internal entity ids, not the numbers users see. A
//     pull request's /pulls/{requestId} routes take its "id"; its "number" is
//     per-project. resolveRequestID maps between them and caches the result.
//
// # No conditional requests, and what the guards do instead
//
// OneDev returns neither ETag nor Last-Modified on API responses. This was
// verified against a live instance. The client therefore has no
// If-None-Match / If-Modified-Since plumbing at all — adding it would send
// headers the server ignores and invite callers to assume a 304 path that can
// never fire.
//
// Both guard methods synthesise their own validator instead, hashing a cheap
// query's result into the token the observer round-trips. They degrade towards
// reporting "changed": an empty caller token, an unavailable probe, or an
// ambiguous result all lead to a full fetch rather than a claim of freshness
// the transport cannot back.
//
// The two guards are not equally strong, and the difference is documented on
// each method. RepoPRListGuard's token is sound — every change to a pull
// request bumps its last-activity date, so the newest request's identity and
// timestamp cannot stay fixed across a change. CommitChecksGuard's is an
// optimisation only: OneDev has no per-commit build lookup AO can rely on (the
// global build query's "Commit" criterion answers HTTP 404 "Unable to find
// revision" even for commits that exist) and builds carry no last-activity
// field, so the guard hashes a bounded window of the project's recent builds.
// The observer's DefaultPRMaxAge backstop, not this guard, is what bounds CI
// staleness.
//
// This is a deliberate trade rather than a gap. GitHub needs conditional
// requests because it is rate-limited and bills every unconditional call; a
// self-hosted OneDev generally is not, so polling it is cheap and correctness
// is worth more than the round-trips saved.
//
// # No rate-limit signal
//
// The client models a 429 (RateLimitError, satisfying the observer's
// GetRetryAfter/GetResetAt capability) so that a reverse proxy or WAF in front
// of an instance is handled correctly. OneDev itself does not throttle the
// REST API and emits no rate-limit headers of its own, so in a direct
// deployment that path never fires. It is defence for the proxy case, not a
// claim that OneDev reports quota.
//
// # Inline review threads are not available
//
// OneDev exposes code comments only as GET /~api/code-comments/{commentId}:
// there is no per-request listing and no query resource, so an inline thread
// cannot be enumerated from a pull request. FetchReviewThreads returns review
// verdicts and request-level comments, and marks the observation Partial so
// the store merges those rows rather than deleting the threads AO cannot see.
package onedev
