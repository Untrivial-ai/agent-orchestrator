// Package gitcode implements the SCM provider contract for GitCode
// (gitcode.com) using its public v5 REST API.
//
// The v5 API is Gitee-shaped: pull requests live under
// /api/v5/repos/{owner}/{repo}/pulls, browser URLs use the
// /{owner}/{repo}/merge_requests/{number} path, and pagination is
// signaled through the total_count / total_page response headers rather
// than a Link header. GitCode does not expose CI pipeline or review
// endpoints through v5, so the adapter reports CI as unknown, derives
// review threads from PR comments grouped by discussion_id, and leaves
// the CI column blank on the Kanban.
package gitcode
