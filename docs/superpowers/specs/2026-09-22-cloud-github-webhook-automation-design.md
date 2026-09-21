# Cloud GitHub webhook automation — design

## Purpose

Phase 2 makes GitHub changes visible to AO Cloud without waiting for the
control plane's periodic pull-request scanner. A GitHub App delivery updates
the durable pull-request facts that already drive the Cloud right panel and
Kanban board. A newly failing check is always recorded; it is sent to the
session's worker only when that session has enabled **Automatically fix CI
failures**.

The result must work identically for Docker, NodeOps, and Coder. Those
providers differ only in sandbox provisioning; all GitHub, policy, persistence,
and worker-transport work remains in the control plane.

## Scope and boundaries

Included:

- GitHub App webhook processing for pull requests, check suites, check runs,
  and external pull-request reviews.
- Durable Cloud PR/check/review facts, notification events, and derived board
  placement.
- A Cloud session-level `autoInjectCI` preference, shown in the existing
  inspector control and defaulting to `true` for parity with the existing
  session policy.
- Durable, idempotent CI-feedback delivery to a live or subsequently resumed
  worker through the existing control-plane terminal-request transport.

Excluded:

- Any edit to `backend/`, local daemon behavior, local storage, or local UI
  routes.
- OAuth callback/onboarding changes. The existing OAuth flow stays as-is.
- GitHub App installation/onboarding consolidation. During testing, an
  administrator installs the already configured App separately.
- A manual "Send to agent" action. Disabling the toggle keeps the failure
  visible but does not enqueue worker feedback.
- Replacing every scanner immediately. The existing 30-second PR scanner stays
  as a recovery backstop for missed events, external providers, and fields not
  supplied by an event; it no longer owns the normal GitHub-App fast path.

## Existing foundations

Cloud already has the durable pieces this design extends:

- `POST /api/cloud/v1/github/webhooks` validates `X-Hub-Signature-256`, bounds
  the body, and stores a delivery in `ao_github_webhook_deliveries`.
- That table deduplicates by GitHub delivery ID, leases a single delivery per
  installation, retries with backoff, and records a terminal failure after ten
  attempts.
- `ao_pull_requests` holds durable PR lifecycle, head SHA, CI state, review
  decision, mergeability, and status observations. Cloud resource responses
  already surface those facts to the existing shared frontend presentation.
- The shared `contract` Kanban reducer derives the board column from session
  policy plus PR facts; it must remain the only placement authority.
- The worker terminal transport already provides durable ordered
  `terminal.input` requests, a live WebSocket fast path, and resume/replay
  behavior.
- Phase 1 supplies durable Cloud notifications, WebSocket hints, and REST
  reconciliation. Webhooks must publish only through that mechanism rather
  than creating a second client channel.

## Architecture and data flow

```text
GitHub App webhook
  -> signed HTTP intake
  -> ao_github_webhook_deliveries (delivery-ID dedupe, lease, retry)
  -> GitHub event processor
     -> map installation + repository + PR to an AO Cloud PR/session
     -> fetch/normalize authoritative PR/check/review snapshot as needed
     -> one transaction: update PR facts, record event/application key,
        create or resolve Cloud notification, enqueue CI feedback when allowed
  -> existing terminal-request transport
     -> live sandbox WebSocket when available, durable replay when not
  -> existing Cloud notification stream + REST recovery
  -> right panel and Kanban re-fetch/reconcile their normal Cloud data
```

GitHub's payload is an event hint, not tenant authority. The processor derives
organization and installation from the server-side installation route, then
matches only the configured GitHub repository and tracked PR number. A delivery
for an unknown or no-longer-tracked repository completes without leaking data
or creating a session.

For `pull_request`, `check_suite`, `check_run`, and `pull_request_review`, the
processor uses the delivery to select the affected tracked PR and refreshes its
authoritative snapshot through the existing installation-scoped GitHub client.
This deliberately reuses the current aggregation rules for CI, review, and
mergeability rather than trying to maintain competing partial reducers from
different webhook shapes. It also handles check events that omit information
required by the right-panel summary.

## State, idempotency, and notifications

The delivery ID prevents duplicate inbound processing. A second durable
application key prevents duplicate side effects when GitHub sends different
deliveries for the same effective CI result. The key is based on the tracked
PR, current head SHA, normalized check identity/conclusion, and the actionable
transition. It is recorded transactionally with the fact update and feedback
outbox row.

The processor compares the prior durable observation with the new one:

- A transition to failing creates or refreshes one unresolved Cloud
  notification for that PR/head/check set and marks the session as CI-failing
  through normal derived facts.
- A transition from failing to passing/neutral resolves that notification.
- Repeated completed, pending, or unchanged failures do not create a new
  notification or another worker instruction.
- PR opened, synchronized, closed, merged, and review events update their
  durable facts and cause the usual frontend refresh; only meaningful
  user-facing transitions create notifications.

Notification deduplication is control-plane work. The client may collapse a
fast WebSocket hint with its durable notification, but it never decides whether
an event is new. If its stream is disconnected, it uses the Phase 1 notification
API and normal Cloud resource fetches to recover.

## Automatic CI feedback

`autoInjectCI` is a durable **Cloud session** preference. It defaults to true
when a new Cloud session is created and is returned with Cloud session data.
The existing inspector policy row becomes cloud-aware and updates the Cloud
control-plane endpoint optimistically, without using the local daemon route.

On a newly actionable CI failure:

1. The processor always persists the observed failure and notification.
2. If `autoInjectCI` is false, it completes with no worker request. The UI can
   communicate that the failure is not being injected.
3. If it is true, the same transaction adds a feedback-outbox row containing a
   concise, structured prompt: PR URL/number, head SHA, failed check names,
   conclusions, URLs, and an instruction to investigate and fix the failure.
4. A dispatcher leases that row and converts it to the existing session
   terminal-input request. The terminal stream immediately forwards it to a
   connected worker; otherwise the request stays durable and is replayed after
   the session resumes.
5. The dispatcher marks the row delivered only after the terminal-request
   record is durably accepted. Retried webhook deliveries cannot enqueue another
   prompt for the same application key.

No UI button can bypass this policy. A worker never receives a CI-failure
instruction merely because the user viewed the notification.

## UI and Kanban behavior

The existing Cloud data adapters continue to feed the shared inspector, right
panel, and board components. Phase 2 adds Cloud-backed session policy and
freshness updates only; it does not fork visual logic or replace local behavior.

When the durable facts change, clients invalidate/reconcile the affected Cloud
session/PR query. The right panel shows the changed CI/review state and failed
checks. The Kanban board calls the existing shared reducer using those facts:

- failing CI + `autoInjectCI=true` is AO-owned and appears as validating /
  fixing CI;
- failing CI + `autoInjectCI=false` remains visible but is human-owned and
  appears as needs review / CI failing;
- a recovered check moves the card according to the remaining PR facts.

This avoids persisting a separate Cloud display status and preserves the
repository invariant that board status is derived from durable facts.

## Error handling and security

- Intake rejects missing headers, oversized payloads, and invalid signatures
  before persistence. Payloads remain server-side only.
- The webhook worker is lease-safe across multiple control-plane replicas and
  retains ordered processing per installation.
- Unknown installations, repositories, or untracked PRs are safe no-ops after
  audit/logging at an appropriate level; they must not be retried forever.
- GitHub API failures, temporary database errors, and unavailable worker
  transport retry through the existing delivery/outbox lease mechanisms with
  bounded exponential backoff. Permanent malformed/unmappable event data is
  terminally recorded for operators.
- The webhook endpoint is publicly reachable only for GitHub; it relies on the
  configured webhook secret, not user authentication. A temporary public
  tunnel is valid for local testing only. Production uses the configured HTTPS
  Cloud public URL.

## Test plan

- HTTP tests: signature, required headers, body cap, duplicate delivery, and
  supported-event acceptance.
- GitHub processor tests: installation/repository/PR routing, event-to-refresh
  behavior, no-op unknown objects, retries, and no cross-org access.
- PostgreSQL store tests: atomic fact update, transition/application-key
  dedupe, notification creation/resolution, feedback-outbox leasing, and
  replay-safe dispatch.
- Worker transport tests: queued CI feedback reaches a live terminal and a
  disconnected worker receives it after reconnect, exactly once per key.
- Frontend tests: Cloud policy toggle uses the Cloud endpoint with immediate
  optimistic state; Cloud right-panel/board data displays both injected and
  non-injected CI failures without falling through to the local API.
- Integration test with a signed fixture delivery verifies durable Cloud
  notification, PR/board refresh, and conditional worker feedback for Docker,
  NodeOps, and Coder through the provider-neutral control plane.

## Acceptance criteria

- A signed GitHub CI failure for a tracked Cloud PR becomes visible quickly in
  the Cloud right panel, notification list, and derived Kanban state.
- The failure is durable and visible even when automatic fixing is disabled.
- With automatic fixing enabled, the active worker receives one actionable
  instruction; with it disabled, it receives none.
- Duplicate deliveries, reconnects, retries, and repeated unchanged check
  events cannot create duplicate notifications or prompts.
- Docker, NodeOps, and Coder use the same control-plane implementation.
- Local behavior, local storage, and OAuth/onboarding behavior are unchanged.
