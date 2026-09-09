// Package gitlab implements the ports.Tracker outbound port for GitLab
// Work Items via the GraphQL API. v1 is read-only:
//
//   - Get returns a normalized snapshot of one work item (spawn-bootstrap
//     reads it to hydrate the agent prompt).
//   - List returns a filtered slice of work items in a project, paginated
//     via cursor-based pagination (first/after with pageInfo.endCursor /
//     hasNextPage) with state/labels/assignee filters.
//   - Preflight performs a single REST GET /user against GitLab to verify
//     the token is accepted; success is cached for the lifetime of the
//     Tracker, failures are not. Preflight stays on REST because it
//     validates token validity, not Work Items access.
//
// Get and List send GraphQL queries to POST /api/v4/graphql. Work item
// details (assignees, labels) are exposed as typed widgets in the
// response (WorkItemWidgetAssignees, WorkItemWidgetLabels).
//
// The adapter reuses the SCM provider's TokenSource chain
// (AO_GITLAB_TOKEN / GITLAB_TOKEN / glab auth status --show-token).
//
// # State mapping
//
// GitLab work items have two native states: opened and closed. They map
// onto the normalized state vocabulary as follows:
//
//   - opened -> open
//   - closed -> done
//
// # Out of scope
//
//   - No Comment, no Transition.
//   - No ETag-based conditional revalidation (unlike the GitHub tracker);
//     List always fetches fresh data.
//   - No webhook receiver, no polling goroutine.
package gitlab
