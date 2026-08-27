// Package onedev implements the ports.Tracker outbound port for OneDev
// issues. Like the GitHub and GitLab trackers it is read-only:
//
//   - Get returns a normalized snapshot of one issue.
//   - List returns a filtered slice of a project's issues, paginated with
//     OneDev's offset/count paging.
//   - Preflight performs one cheap authenticated GET against every configured
//     instance to verify the credential is accepted; success is cached for the
//     lifetime of the Tracker, failures are not.
//
// The adapter reuses the OneDev SCM provider's credential model
// (internal/adapters/scm/onedev.TokenSource, which resolves an access token or
// a basic-auth pair) so one deployment configures OneDev credentials once.
//
// # Always self-hosted, so the host is never optional
//
// There is no onedev.com. Every instance is self-hosted, so — exactly as in
// the SCM provider — AllowedHosts is required configuration and New fails with
// ErrNoAllowedHosts when it is empty. Every TrackerID and TrackerRepo this
// adapter accepts or produces carries a populated Host; the zero value has no
// meaning here, unlike GitLab where "" means gitlab.com.
//
// The host allowlist is parsed here rather than borrowed from the SCM package
// because those helpers are unexported and the SCM adapter is out of scope for
// this change. The two parsers accept the same entry syntax
// ("host", "host:port", "http://host:port") and both default a bare entry to
// https, so one AO_ONEDEV_ALLOWED_HOSTS value configures both.
//
// # Query language
//
// Issue queries and pull-request queries are NOT symmetric, and the difference
// is the single easiest thing to get wrong when reading the SCM provider first:
//
//   - For issues, "State" is a queryable field and a bare "open" criteria is
//     rejected with HTTP 406 "Invalid query".
//   - For pull requests it is the other way round — a bare "open" works and
//     "Status" is not a field.
//
// Unknown field names are a hard error (HTTP 500 "Field not found: X"), but an
// unknown *value* is not: `"State" is "Bogus"` simply matches nothing. That
// asymmetry is what makes the configurable state mapping below safe to send
// straight into a query.
//
// Ids in paths are OneDev's internal entity ids, not the per-project numbers
// users see, so Get resolves "project#number" through a query rather than
// GET /~api/issues/{number}. The query returns the full issue, so this costs no
// extra round-trip.
//
// # State mapping
//
// OneDev issue states are configured per instance, not fixed by the product:
// one deployment has Open/Closed, another adds In Progress, In Review, Blocked
// or anything else its workflow needs. There is therefore no two-value mapping
// to hardcode. DefaultStateMap ships the obvious names and
// AO_ONEDEV_ISSUE_STATES overrides or extends it — see states.go.
//
// A native state that is in neither is normalized to domain.IssueOpen, and the
// direction of that default is deliberate. The two candidate defaults fail very
// differently:
//
//   - Defaulting to done makes AO treat live work as finished. An issue in a
//     bespoke "Blocked" or "Needs Design" state silently drops out of intake
//     and out of every open-issue view, and nothing anywhere reports it.
//   - Defaulting to open makes AO treat finished work as live. The worst case
//     is an already-resolved issue appearing in a list, which a human sees and
//     can correct — and intake's own duplicate suppression stops it spawning a
//     second session for an issue it has already picked up.
//
// The recoverable failure wins. Operators whose instance has extra terminal
// states map them explicitly with AO_ONEDEV_ISSUE_STATES.
//
// # Labels and assignees are custom fields
//
// OneDev issues have no labels and no built-in assignee. Both live in
// per-instance custom fields (GET /~api/issues/{id}/fields), which the default
// issue template populates as Type, Priority and Assignees.
//
// The adapter therefore reads that endpoint per issue and projects it onto the
// normalized shape: the assignee field (AssigneeField, default "Assignees")
// becomes Issue.Assignees, and every other field value becomes a
// "Field: Value" entry in Issue.Labels. ListFilter.Labels is matched against
// those client-side — case-insensitively, against either the whole
// "Field: Value" label or just its value — because field names differ per
// instance and cannot be turned into a server-side criterion without risking
// the "Field not found" 500 above.
//
// That costs one extra request per returned issue. It is accepted for the same
// reason the SCM provider accepts unconditional polling: a self-hosted OneDev
// is not rate-limited or billed per call, and Issue.Assignees is load-bearing
// for intake's eligibility rule, so guessing it is empty would be worse than
// paying for it. A failure fetching the fields fails the whole call rather than
// yielding an issue with silently-empty assignees.
//
// # Out of scope
//
//   - No Comment, no Transition — the adapter is read-only.
//   - No conditional requests: OneDev sends neither ETag nor Last-Modified.
//   - No webhook receiver, no polling goroutine.
package onedev
