# Cloud PR webhook local-parity design

## Purpose

AO Cloud must expose the same pull-request facts, right-panel presentation,
derived session status, notifications, and automatic worker feedback as the
local daemon. The transport differs: GitHub App webhooks are Cloud's normal
fast path, while the existing 30-second scanner remains unchanged as a
reconciliation fallback.

The implementation is provider-neutral above the GitHub adapter and applies
equally to Docker, NodeOps, and Coder. No file under `backend/` and no local
daemon behavior changes.

## Behavioral parity contract

Cloud stores and renders every PR fact consumed by the local read model:

- PR identity and metadata: provider ID, canonical/browser URL, number, title,
  author/avatar, draft/open/closed/merged state, source and target branches,
  head/base/merge SHAs, diff statistics, and provider timestamps.
- CI: the aggregate state, all visible check runs and commit status contexts,
  failing checks, URLs, conclusions, and failure fingerprints.
- Reviews: aggregate decision plus each reviewer's latest submitted review,
  including `COMMENTED` reviews, body, URL, target SHA, bot identity, and time.
- Review feedback: inline threads, file and line, comments and replies,
  resolved/outdated state, bot identity, and the policy captured when feedback
  was first observed.
- Mergeability: normalized state and blockers, including conflicts,
  behind-base, draft, CI, review, and unresolved-comment blockers.

All of these facts update the shared right-side PR panel and existing derived
Kanban/session status. Cloud does not persist a separate display status.

Automatic worker feedback follows local semantics:

- Newly failing CI is sent only when automatic CI fixing is enabled.
- Actionable unresolved human inline comments are sent only when automatic
  review fixing is enabled.
- A changes-requested review is sent only when automatic review fixing is
  enabled.
- A non-decisive general `COMMENTED` review is displayed but is not sent by
  itself.
- Merge conflicts produce the same rebase instruction and deduplication
  behavior as local.

Bell notifications also follow local exactly. The supported PR transitions
are `ready_to_merge`, `pr_merged`, and `pr_closed_unmerged`; `needs_input`
continues to come from agent activity. PR-open and CI-failure transitions
remain visible in the panel/status but do not create bell notifications. Cloud
stops producing new `pr_opened` and `ci_failed` bell rows; existing historical
rows remain readable and are not destructively deleted.

## Webhook fast path

The signed webhook endpoint remains a durable intake boundary. It validates
the signature, stores the raw delivery once by GitHub delivery ID, and returns
quickly. Processing derives organization, installation, and repository from
server-owned installation routing; event payload tenant identifiers are never
trusted as authority.

Relevant invalidation events are:

- `pull_request` for lifecycle, metadata, head changes, and mergeability;
- `pull_request_review` for submitted, edited, and dismissed reviews;
- `pull_request_review_comment` for inline comment creation, edits, and
  deletion;
- `pull_request_review_thread` for thread resolution and reopening;
- `check_run` and `check_suite` for GitHub Checks;
- `status` for legacy/external commit status contexts; and
- `push` for base/head changes that can alter tracked PR mergeability.

An event is an invalidation hint, not a partial state mutation. After resolving
the affected tracked PR or tracked PRs, the processor fetches an authoritative
GitHub snapshot using the installation token, normalizes it to the same fact
model as local, then applies that snapshot transactionally. This prevents
payload-shape differences, out-of-order deliveries, and edits/deletions from
creating a second set of reducers.

PR-specific events refresh one tracked PR. A check/status event uses its SHA
and repository to refresh matching tracked PRs. A push refreshes only tracked,
open PRs in that repository whose head or base can be affected. Unknown or
untracked objects complete as safe no-ops.

## Persistence and read model

Cloud migrations add normalized review-summary and review-comment storage and
extend the existing review-thread storage where required. Existing
`ao_pull_requests` remains the aggregate row; normalized check, review, thread,
and comment rows are children of it. Every table remains organization-scoped
and protected by the existing row-level security model.

Snapshot application happens in one transaction:

1. Load the previous authoritative snapshot.
2. Upsert PR metadata and aggregate states.
3. Replace or merge normalized checks, review summaries, threads, and comments
   according to whether the GitHub fetch was complete.
4. Compute semantic transitions against the previous snapshot.
5. Record dedupe/application keys and enqueue eligible worker feedback.
6. Create or resolve only local-parity notification types.
7. Touch the owning session and publish durable notification/session events.

The Cloud PR summary endpoint returns the same semantic fields used by the
shared local inspector. The frontend Cloud adapter only maps names and routes;
it does not derive missing SCM facts or decide deduplication.

## Deduplication and delivery

Inbound deduplication stays keyed by GitHub delivery ID. Side effects use
semantic application keys so two different GitHub deliveries describing the
same state cannot duplicate work:

- CI: PR ID + head SHA + failing-check fingerprint;
- review summary: PR ID + provider review ID + effective state/body revision;
- inline feedback: PR ID + provider comment ID + revision/resolution state;
- conflict: PR ID + head/base identity + conflicting state; and
- notifications: PR ID + notification type + relevant transition identity.

Worker instructions use a provider-neutral SCM feedback outbox and the
existing durable terminal-input transport. Rows are marked delivered only
after the terminal request is durably accepted. A disconnected worker receives
the instruction after reconnect; retries do not create another terminal input.
Policy is captured on newly observed review/comment records, matching local's
behavior when a toggle changes after feedback was received.

## Notifications and recovery

Durable notification creation/resolution happens in the control plane.
PostgreSQL remains authoritative, SSE carries immediate created/resolved
events, and the REST notification API reconciles after disconnects. The client
may replace a pending stream hint with a durable row but never determines
whether an SCM event is new.

`ready_to_merge` is created only when the same shared readiness rule used by
local is true: the PR is open and non-draft, CI is passing, no change request
or actionable unresolved human comment exists, and mergeability is mergeable.
It resolves when any blocker returns or the PR becomes terminal. Merge and
closed-unmerged notifications are terminal transition notifications and are
deduplicated once per transition.

The 30-second scanner is not removed, slowed, or otherwise modified. It calls
the same authoritative refresh/application path and repairs missed webhook
deliveries without owning normal latency.

## Error handling and security

- Invalid signatures, missing delivery headers, unsupported events, and
  oversized bodies are rejected before enqueueing.
- Delivery leases and bounded retry/backoff remain safe across multiple
  control-plane replicas.
- GitHub API/rate-limit failures retain the previous durable snapshot and
  retry; an unknown probe is never treated as a closed or dead PR.
- Partial check/review fetches never erase facts outside the fetched window.
- Out-of-order webhook deliveries converge because every delivery reads the
  current authoritative snapshot before applying transitions.
- Comment and review bodies are treated as attacker-controlled. Control bytes
  are stripped before terminal injection, matching local.
- Installation/repository routing and every database write remain scoped to
  the derived organization. No payload may select another tenant.

## UI behavior

The existing shared inspector remains the visual authority. Cloud supplies the
complete summary so the Reviews area shows submitted reviews (including the
user's `COMMENTED` review), unresolved and resolved inline feedback, reviewer,
body, file/line, URLs, bot state, and whether each item was eligible for
automatic injection.

Webhook-driven durable events invalidate the affected Cloud session and PR
queries immediately. The right panel and derived board therefore update
without waiting for the fallback scanner. No local API route is called for a
Cloud session, and no duplicate Cloud-only PR presentation is introduced.

## Test strategy

Implementation is test-first and covers:

- intake acceptance and PR extraction for every supported event;
- installation/repository/PR routing and cross-organization isolation;
- authoritative snapshot normalization for metadata, status contexts, checks,
  submitted reviews, threads, comments, and mergeability;
- PostgreSQL replacement/merge semantics, RLS, and application-key dedupe;
- local-parity worker-injection decisions for CI, reviews, comments, and
  conflicts, including disabled toggles and reconnect replay;
- local-parity notification creation and resolution, including proof that PR
  open, CI failure, and ordinary comments do not create bell rows;
- SSE plus REST recovery and immediate Cloud query invalidation;
- shared inspector rendering from the Cloud summary; and
- end-to-end signed webhook fixtures exercising the provider-neutral flow used
  by Docker, NodeOps, and Coder.

The relevant Cloud Go suite, frontend targeted tests, frontend typecheck, Go
vet, and full suites are run before handoff. Environment-related failures are
reported explicitly rather than represented as passing.

## Acceptance criteria

- Any GitHub change represented in local's PR observation model reaches the
  Cloud durable model through a webhook-triggered authoritative refresh.
- The right panel and derived session/Kanban status match local for the same
  facts and update normally within a few seconds.
- Automatic worker feedback and deduplication match local's policy.
- Bell notifications match local's supported types and transition rules.
- Missed or delayed webhooks self-heal through the unchanged 30-second scan.
- Docker, NodeOps, and Coder share one control-plane implementation.
- No local backend behavior or storage is changed.
