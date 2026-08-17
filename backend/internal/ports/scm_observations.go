// This file defines provider-neutral SCM DTOs used at the boundary between the
// SCM observer, persistence layer, and lifecycle manager. Provider adapters fill
// these structs with normalized facts so downstream code does not depend on raw
// GitHub payloads or GitHub-specific enum names.
package ports

import (
	"context"
	"errors"
	"time"
)

// ErrSCMNotFound is the provider-neutral sentinel for successful SCM lookups
// that found no matching resource, such as a branch with no open pull request.
var ErrSCMNotFound = errors.New("scm: not found")

// SCMRepo identifies a repository without assuming a provider-specific URL
// shape. Repo is conventionally "owner/name" for providers that expose an
// owner namespace, while Owner/Name are kept split for provider calls.
type SCMRepo struct {
	// Provider is the normalized SCM provider name, e.g. "github".
	Provider string
	// Host is the SCM host, e.g. "github.com" or a GitHub Enterprise host.
	Host string
	// Owner is the provider-specific namespace/organization/user.
	Owner string
	// Name is the repository name without the owner namespace.
	Name string
	// Repo is the display/stable full repository name, usually "owner/name".
	Repo string
}

// SCMPRRef identifies a pull request within a provider-neutral repository.
type SCMPRRef struct {
	// Repo is the normalized repository that owns the pull request.
	Repo SCMRepo
	// Number is the provider's pull request number within the repository.
	Number int
	// URL is the canonical browser URL when already known locally.
	URL string
}

// SCMGuardResult is an ETag-style cache guard result. NotModified maps to HTTP
// 304 for providers that support it.
type SCMGuardResult struct {
	// ETag is the latest provider cache validator for this guard endpoint.
	ETag string
	// NotModified is true when the provider reported no change since the ETag.
	NotModified bool
}

// SCMObservation is the provider-neutral pull-request observation emitted by
// the SCM observer and consumed by lifecycle. Provider adapters normalize their
// SCM-specific payloads into this DTO before the observer persists/notifies.
type SCMObservation struct {
	// Fetched is true only when the provider refresh succeeded and the nested
	// facts are authoritative for this poll.
	Fetched bool
	// ObservedAt is the observer timestamp for this normalized snapshot.
	ObservedAt time.Time

	// Provider is the normalized SCM provider name, e.g. "github".
	Provider string
	// Host is the SCM host that served this observation.
	Host string
	// Repo is the full repository name shown to AO users, usually "owner/name".
	Repo string

	// PR contains pull-request metadata such as branches, title, state, and diff stats.
	PR SCMPRObservation
	// CI contains the rolled-up CI state, checks, failing fingerprint, and log tail.
	CI SCMCIObservation
	// Review contains review decision plus normalized review threads/comments.
	Review SCMReviewObservation
	// Mergeability contains AO's mergeability verdict and blockers.
	Mergeability SCMMergeabilityObservation

	// Error carries a transient per-observation failure from the multi-provider
	// dispatcher (or any composite provider) when one provider in a batch fails
	// while others succeed. It is NOT durable state: the observer must inspect
	// it before persistence to route rate-limit errors to per-provider cooldown
	// and non-rate-limit errors to refresh-incomplete, then nil it out so the
	// storage layer never sees provider-error classification. A non-nil Error
	// always implies Fetched=false.
	Error error

	// Changed marks which semantic buckets changed compared with the DB snapshot.
	Changed SCMChanged
}

// SCMChanged marks which semantic state buckets changed in the successful poll.
type SCMChanged struct {
	// Metadata is true when PR metadata or mergeability facts changed.
	Metadata bool
	// CI is true when check/CI facts or failure logs changed.
	CI bool
	// Review is true when review decision, threads, or comments changed.
	Review bool
}

// SCMIdentity describes the account authenticated with the SCM provider.
type SCMIdentity struct {
	// Login is the provider login/name of the authenticated account.
	Login string
	// Human is true when the provider identifies the account as a human user.
	Human bool
}

// SCMIdentityResolver lazily resolves the account authenticated with the
// active SCM provider.
type SCMIdentityResolver interface {
	AuthenticatedIdentity(ctx context.Context) (SCMIdentity, error)
}

// ScopedIdentityResolver resolves the authenticated identity for a specific
// provider key and host. Multi-provider implementations use this to delegate
// to the matching sub-provider's identity method, passing host through so
// that self-managed GitLab hosts resolve identity against the correct client.
// GitHub sub-providers ignore host (their identity is not host-scoped).
type ScopedIdentityResolver interface {
	AuthenticatedIdentityForProvider(ctx context.Context, provider, host string) (SCMIdentity, error)
}

// SCMPRObservation carries provider-neutral PR metadata.
type SCMPRObservation struct {
	// ProviderID is the provider-owned immutable identifier for this PR/MR.
	// Consumers scope it by the observation's provider and host.
	ProviderID string
	// URL is the canonical PR URL used as the persistence key.
	URL string
	// URLAlias is the previously requested URL when it resolved to URL.
	URLAlias string
	// Number is the provider's PR number in the repository.
	Number int
	// State is AO's normalized PR state: draft, open, merged, or closed.
	State string
	// Draft is true when the PR is marked draft/work-in-progress.
	Draft bool
	// Merged is true when the PR has been merged.
	Merged bool
	// Closed is true when the PR is closed without being merged.
	Closed bool
	// SourceBranch is the PR head/source branch name.
	SourceBranch string
	// HeadRepo is the full name (owner/name) of the repository the PR head
	// branch lives in. It matches the base repo for same-repo PRs and differs
	// for PRs opened from a fork, so branch-prefix attribution can ignore forks.
	HeadRepo string
	// TargetBranch is the PR base/target branch name.
	TargetBranch string
	// HeadSHA is the current head commit SHA for the PR.
	HeadSHA string
	// Title is the provider PR title.
	Title string
	// Additions is the provider-reported added line count.
	Additions int
	// Deletions is the provider-reported deleted line count.
	Deletions int
	// ChangedFiles is the provider-reported changed file count.
	ChangedFiles int
	// Author is the provider login/name of the PR author.
	Author string
	// BaseSHA is the current base branch SHA when the provider supplies it.
	BaseSHA string
	// MergeCommitSHA is the merge commit SHA when the PR has one.
	MergeCommitSHA string

	// ProviderState preserves the provider's raw PR state enum/string.
	ProviderState string
	// ProviderMergeable preserves the provider's raw mergeable enum/string.
	ProviderMergeable string
	// ProviderMergeStateStatus preserves provider-specific merge status detail.
	ProviderMergeStateStatus string
	// HTMLURL is the provider browser URL; it usually matches URL.
	HTMLURL string

	// CreatedAtProvider is the provider's PR creation timestamp.
	CreatedAtProvider time.Time
	// UpdatedAtProvider is the provider's last PR update timestamp.
	UpdatedAtProvider time.Time
	// MergedAtProvider is the provider's merge timestamp when merged.
	MergedAtProvider time.Time
	// ClosedAtProvider is the provider's close timestamp when closed.
	ClosedAtProvider time.Time
}

// SCMCIObservation carries aggregate CI state plus failing-check details.
type SCMCIObservation struct {
	// Summary is AO's normalized aggregate CI state: unknown, pending, passing, or failing.
	Summary string
	// HeadSHA is the commit SHA that the check data applies to.
	HeadSHA string
	// FailedFingerprint is a stable semantic signature of current failing checks.
	FailedFingerprint string
	// Checks contains all normalized visible check/status contexts.
	Checks []SCMCheckObservation
	// FailedChecks contains only failing/cancelled checks that may need agent action.
	FailedChecks []SCMCheckObservation
	// FailureLogTail is the combined tail of newly fetched failed-check logs.
	FailureLogTail string
	// Partial is true when the CI check listing was truncated by the
	// provider's pagination cap (e.g. more than 1,000 pipeline jobs). When
	// true, Checks/FailedChecks are a bounded snapshot, not an authoritative
	// complete CI state — the observer must not overwrite durable checks as
	// Fetched=true complete (review Item 13). Mirrors SCMReviewObservation.Partial.
	Partial bool
}

// SCMCheckObservation is one normalized check/status context. ProviderID is an
// optional provider-owned identifier (for GitHub, Actions job/check-run id) used
// by the provider to fetch logs; consumers should not attach meaning to it.
type SCMCheckObservation struct {
	// Name is the check run name or commit status context name.
	Name string
	// Status is AO's normalized check status.
	Status string
	// Conclusion is the provider conclusion/state string preserved for detail.
	Conclusion string
	// URL is a provider link to the check/status details.
	URL string
	// LogTail is the last 20 lines of a failed job log when fetched.
	LogTail string
	// ProviderID is an opaque provider id used for follow-up provider calls.
	ProviderID string
}

// SCMReviewObservation carries normalized review-decision and review-thread facts.
type SCMReviewObservation struct {
	// Decision is AO's normalized review decision.
	Decision string
	// Reviews contains submitted review summaries fetched on the slower review cadence.
	Reviews []SCMReviewSummaryObservation
	// Threads contains normalized review threads fetched on the slower review cadence.
	Threads []SCMReviewThreadObservation
	// Partial is true when the provider intentionally fetched and persisted a
	// bounded review-thread window instead of a complete PR-lifetime snapshot.
	// Consumers should treat Threads as a merge/update set in that case.
	Partial bool
}

// SCMReviewSummaryObservation is one submitted review with its provider summary URL.
type SCMReviewSummaryObservation struct {
	// ID is the provider's stable submitted-review identifier.
	ID string
	// Author is the provider login/name of the reviewer.
	Author string
	// State is AO's normalized review decision for this review.
	State string
	// URL is a provider link to the submitted review summary.
	URL string
	// Body is the reviewer's submitted summary text, empty when the provider
	// review carried no body.
	Body string
	// TargetSHA is the PR head commit SHA this provider review was submitted
	// against, when the provider exposes it.
	TargetSHA string
	// IsBot is true when the provider identifies the reviewer as a bot.
	IsBot bool
	// SubmittedAt is the provider's review submission timestamp.
	SubmittedAt time.Time
}

// SCMReviewThreadObservation is a normalized review thread with comments.
type SCMReviewThreadObservation struct {
	// ID is the provider's stable review thread identifier.
	ID string
	// Path is the file path the thread is anchored to.
	Path string
	// Line is the line number the thread is anchored to when supplied.
	Line int
	// Resolved is true when the provider marks the thread resolved.
	Resolved bool
	// IsBot is true when the thread's comments are all/primarily bot-authored.
	IsBot bool
	// Comments contains normalized comments in this review thread.
	Comments []SCMReviewCommentObservation
}

// SCMReviewCommentObservation is one normalized review comment.
type SCMReviewCommentObservation struct {
	// ID is the provider's stable review comment identifier.
	ID string
	// Author is the provider login/name of the commenter.
	Author string
	// Body is the review comment text.
	Body string
	// URL is a provider link to the comment.
	URL string
	// IsBot is true when the provider identifies the author as a bot.
	IsBot bool
}

// SCMMergeabilityObservation is the normalized mergeability verdict.
type SCMMergeabilityObservation struct {
	// State is AO's normalized mergeability state.
	State string
	// Mergeable is true when the PR is currently mergeable.
	Mergeable bool
	// Conflict is true when merge conflicts block merging.
	Conflict bool
	// BehindBase is true when the head branch is behind the base branch.
	BehindBase bool
	// Blockers lists normalized reasons preventing merge.
	Blockers []string
}
