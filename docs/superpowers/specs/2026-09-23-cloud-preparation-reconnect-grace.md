# Spec: Cloud preparation reconnect grace

Status: implemented, 2026-09-23

Builds on:

- `docs/superpowers/specs/2026-09-21-cloud-cold-start-ux.md`
- `frontend/src/renderer/components/TaskComposer.tsx`
- `frontend/src/renderer/lib/cloud-session-preparation.ts`
- `cloud/internal/httpapi/resource_handlers.go`
- `cloud/internal/postgres/project_session_store.go`
- `cloud/internal/postgres/sandbox_store.go`

This spec supersedes the immediate-close cleanup rule in D11 of the Cloud
cold-start specification. Closing an unsubmitted composer detaches it from its
preparation. It does not immediately delete the preparation. A compatible
composer may reconnect during a bounded two-minute grace period.

---

## 1. Goal and non-goals

### 1.1 Goal

Preserve the cold-start work triggered by New Task when a user accidentally
closes and quickly reopens the composer. Keep the prepared session only while
the composer is active or during a two-minute reconnect grace. Reclaim the
sandbox automatically when the user remains idle or does not return.

The intended experience is:

```text
User opens New Task
  -> hidden cold session starts
  -> checkout and harness preparation advance
  -> user closes the composer
  -> preparation remains hidden for two minutes
  -> user reopens with compatible settings
  -> the same preparation reconnects
  -> Start Task commits the same session and first prompt once
```

If the user does not return:

```text
Composer becomes inactive or closes
  -> lease renewal stops
  -> two-minute grace expires
  -> sandbox deletion is requested
  -> provider resources are reclaimed
```

### 1.2 Non-goals

- Shared warm pools, standby workers, or speculative sessions before New Task.
- Reusing a preparation across users, organizations, projects, harnesses, or
  sandbox providers.
- Keeping an idle composer alive indefinitely.
- Recovering a preparation after its server lease has expired.
- Reattaching after an application restart in the first implementation slice.
- Persisting prompt text, attachments, or draft content in the preparation
  lease.
- Changing checkout, harness startup, terminal readiness, or browser intent
  rules.

## 2. Decisions

### D1. A preparation remains user-triggered cold compute

New Task is still the allocation boundary. No sandbox exists before that user
action. Retaining the resulting session for a short recovery window does not
create shared or speculative capacity.

### D2. The server lease is authoritative

Both the hidden session and sandbox carry the same preparation expiry. A lease
renewal updates both timestamps in one database transaction. The renderer may
cache an expiry for display and scheduling, but it cannot extend or resurrect a
preparation without a successful server response.

### D3. Active use renews, visibility alone does not

The renderer requests activity renewal only when all of these are true:

- the Cloud composer is mounted;
- the preparation has not been committed, cancelled, or expired;
- meaningful composer activity occurred since the last successful renewal.

Meaningful activity includes opening the composer, editing the prompt, changing
an attachment, or interacting with a task setting. A background window or an
untouched open composer does not renew forever. Activity renewals are trailing
and coalesced so sustained typing does not send one request per keystroke. A
timer firing by itself is never enough reason to renew.

### D4. Close means detach with grace

Closing or unmounting the composer releases its renderer attachment. The
renderer makes one best-effort renewal so the grace deadline is two minutes
from close, then stops renewing. It retains the preparation handle in a
process-scoped registry until that deadline.

The server remains the cleanup authority if the close renewal is lost. In that
case, the preparation may expire sooner based on its last successful renewal.

### D5. Compatible reopen reuses the same preparation

The renderer registry keys preparations by their compute-affecting identity:

```text
organization
user
project
harness
sandbox provider
provider connection
```

Reopening a composer with the same identity acquires the existing handle,
renews it immediately, and keeps the same session identifier. Prompt text,
display name, model tuning, and attachments are not part of this identity.

Changing a compute-affecting value invalidates the old preparation, requests
its deletion, and creates a replacement. A committed or explicitly deleted
preparation is never reusable.

### D6. Reconnect is process-scoped in the first slice

The preparation registry lives above the composer component for the lifetime of
the desktop renderer process. It survives dialog close and route changes. It
does not survive a complete application restart in the first slice.

After a process crash or restart, the server lease expires and reclaims the old
preparation. A newly opened composer creates a new preparation. Server-side
discovery across restarts may be added later without changing the lease
contract.

### D7. Commit remains atomic and exactly once

Start Task uses the existing preparation commit transaction. It clears the
preparation marker, clears both expiry timestamps, reveals the session, and
stores the first prompt as one durable turn.

Ambiguous commit responses retry with the same idempotency key. A second window
that loses a commit race must not create another task automatically. It resolves
the committed session when the idempotent result is available or reports that
the preparation was already consumed.

### D8. Explicit expiry triggers transparent replacement

If the server explicitly reports that the preparation expired before commit,
the renderer keeps the prompt and pending surface, removes the stale registry
entry, and creates one fresh session with a new create idempotency key. It does
not retry the expired session or expose the failure as lost work.

An ambiguous network response is not proof of expiry. The client first retries
the same commit idempotency key. Replacement occurs only after a definitive
expired or deleted response.

### D9. Existing quotas remain in force

A hidden preparation counts as an active sandbox. The renderer permits one
preparation per compatibility key. Server sandbox quotas remain authoritative
for multiple windows, devices, or modified clients.

### D10. Browser startup remains intent-driven

Retaining or renewing a preparation does not start Chromium. Chromium starts
only after the existing explicit browser intent signal.

## 3. Current behavior and required change

| Area | Current behavior | Required behavior |
| --- | --- | --- |
| Ownership | Composer component owns one preparation | Renderer registry owns it across composer mounts |
| Close | Unmount calls delete immediately | Unmount detaches and starts reconnect grace |
| Reopen | Creates a new session | Reuses a compatible unexpired session |
| Expiry | Fixed at two minutes from create | Rolling two-minute lease after meaningful activity |
| Open but idle | Expires, with a stale client handle | Expires, registry evicts, next activity creates fresh |
| Late submit | Commit can fail against expired session | Prompt is preserved and fresh creation starts automatically |
| Config change | Cancels and replaces | Keep this behavior |
| Crash | Server expiry reclaims sandbox | Keep this behavior |

The current database model already has the required expiry on `ao_sessions` and
`ao_sandboxes`. No migration is required unless implementation discovers that
the existing response model cannot expose the authoritative deadline without a
new field.

## 4. State model

### 4.1 Renderer states

```text
none
  -> acquiring
  -> attached
  -> detached_grace
  -> committing
  -> committed

acquiring | attached | detached_grace
  -> expired
  -> invalidated
  -> failed
```

- `acquiring`: preparation request is in flight.
- `attached`: at least one compatible composer owns the handle.
- `detached_grace`: no composer owns the handle, but the server lease remains
  valid.
- `committing`: Start Task claimed the handle and route cleanup cannot release
  it.
- `committed`: the hidden session became an ordinary visible session.
- `expired`: the server rejected renewal or commit as expired.
- `invalidated`: settings changed or the user explicitly discarded the task.
- `failed`: creation failed without a durable session identifier.

### 4.2 Server states

The server continues using durable session and sandbox facts:

```text
hidden and lease valid
  -> renewed
  -> committed
  -> desired deleted

lease expired
  -> desired deleted
  -> provider deletion
  -> terminated
```

An expired preparation cannot transition back to valid. Renewal must reject it
even if provider deletion has not started.

## 5. End-to-end flows

### 5.1 Open, close, and reopen

1. New Task acquires a registry entry for the compatibility key.
2. If none exists, the registry starts the current prepare request.
3. The server creates a hidden session and returns its expiry.
4. Closing the composer releases the entry without calling session deletion.
5. The registry attempts one final renewal and records the returned expiry.
6. Reopening before expiry acquires the same entry.
7. The registry renews immediately before presenting the entry as reusable.
8. If renewal succeeds, the same session continues.
9. If renewal reports expiry, the registry evicts it and starts a fresh
   preparation.

### 5.2 Composer remains active

1. Opening creates a full lease, while reopening renews the retained lease
   immediately.
2. Later meaningful activity marks the lease dirty and schedules a trailing
   renewal.
3. Renewal is rate-limited to at most one request per 45 seconds while retaining
   one trailing request for activity that occurred inside that interval.
4. A successful response replaces the cached expiry with the server value.
5. Transient failures use bounded retry without extending the cached deadline.
6. No further activity means no further renewal.
7. Once the cached deadline passes, the registry treats the preparation as
   expired until the server proves otherwise.

### 5.3 Composer remains open but idle

1. The user stops interacting for two minutes.
2. Renewal stops.
3. The server deadline passes and reconciliation requests deletion.
4. The composer and draft remain visible locally.
5. The next meaningful interaction evicts the expired handle and begins a new
   preparation.
6. Start Task waits for that new session create response, then commits it.

### 5.4 Submit during the reconnect grace

1. The reopened composer acquires the retained preparation.
2. The renderer marks the handle `committing` before route changes unmount the
   composer.
3. Commit uses the original session identifier and stable commit idempotency
   key.
4. Success removes the handle from the preparation registry.
5. The pending route binds to the committed session as it does today.

### 5.5 Submit after expiry

1. Commit returns the explicit preparation-expired error.
2. The pending route and complete prompt remain visible.
3. The renderer removes the expired handle.
4. The renderer creates one fresh session through the normal durable create
   path.
5. The prompt is delivered through exactly one initial-message path.
6. Success binds the pending route to the replacement session.

### 5.6 Incompatible settings

Changing project, harness, sandbox provider, or provider connection performs an
explicit invalidation:

1. Remove the old registry entry.
2. Request deletion for its hidden session when known.
3. Create a new preparation with a new compatibility key.
4. Never carry a prompt or commit idempotency key between the two sessions.

## 6. API contract

### 6.1 Prepare response

The existing prepare response adds authoritative lease metadata:

```json
{
  "session": { "id": "session-id" },
  "preparation": {
    "expiresAt": "2026-09-23T12:00:00Z",
    "leaseSeconds": 120
  }
}
```

The response remains hidden from ordinary session lists. The current create
idempotency behavior remains unchanged.

### 6.2 Renew endpoint

Add:

```text
POST /api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/renew-preparation
```

Successful response:

```json
{
  "preparation": {
    "expiresAt": "2026-09-23T12:02:00Z",
    "leaseSeconds": 120
  }
}
```

The endpoint:

- authenticates the organization and user through the existing principal;
- accepts only an uncommitted, unexpired, non-terminated preparation;
- requires the sandbox desired state to remain `running`;
- updates session and sandbox expiry to one database timestamp plus two
  minutes;
- returns the database timestamp used by the transaction;
- wakes reconciliation only when required by existing session rules;
- never changes display name, prompt, provider, or launch configuration.

### 6.3 Error contract

Use stable error codes:

| Condition | HTTP | Code |
| --- | ---: | --- |
| Preparation expired | 410 | `PREPARATION_EXPIRED` |
| Preparation already committed | 409 | `PREPARATION_COMMITTED` |
| Preparation deleted or unknown | 404 | existing not-found code |
| Wrong organization or user | 404 | existing not-found code |
| Sandbox no longer running | 409 | `PREPARATION_UNAVAILABLE` |

Commit should return `PREPARATION_EXPIRED` for a known expired preparation
instead of collapsing every invalid preparation into a generic not-found
response. It must not disclose a session owned by another principal.

## 7. Storage and concurrency

Add `RenewSessionPreparation` to the Cloud store boundary. Its transaction
locks the matching session and sandbox rows, reads one `clock_timestamp()`, and
updates both expiry columns from that value.

Required invariants:

- session and sandbox expiry are equal after create, renew, and commit;
- commit and renew serialize on the same rows;
- commit wins by clearing the preparation marker and expiries;
- expiry wins by making later renewal and commit fail;
- deletion intent cannot be reversed by renewal;
- retrying renewal can extend the lease again but cannot duplicate any session;
- retrying commit with the same idempotency key returns the original result.

The existing reconciliation query continues converting expired sandbox rows to
`desired_state = 'deleted'`. Provider deletion remains asynchronous and bounded
by the reconcile interval plus provider response time.

## 8. Renderer architecture

Introduce a `CloudSessionPreparationRegistry` outside `TaskComposer`.

The registry owns:

- preparation promises and durable session identifiers;
- compatibility keys;
- create and commit idempotency keys;
- attachment counts;
- last meaningful activity time;
- authoritative lease deadlines;
- renewal timers and bounded retry state.

`TaskComposer` acquires a handle on mount and releases it on unmount. Release no
longer calls the delete endpoint. Configuration changes call `invalidate`,
which still deletes the incompatible preparation.

The registry exposes only bounded operations:

```text
acquire(key)
recordActivity(handle)
release(handle)
invalidate(handle)
commit(handle, input)
```

The registry must not store prompt text. Draft ownership stays in the existing
composer and pending-session systems.

Timers validate the server deadline and schedule from the returned lease length
without assuming the local clock matches the control plane. Activity renewal
runs well before expiry, and the server remains authoritative at the boundary.

## 9. Failure behavior

| Failure | User-visible result | Cleanup |
| --- | --- | --- |
| Final close renewal fails | No error dialog | Last server deadline remains authoritative |
| Periodic renewal times out | Composer remains usable | Retry within existing deadline |
| Server reports expiry | Draft remains editable | Evict handle and prepare fresh on activity |
| Reopen renewal reports expiry | Composer opens normally | Create a replacement preparation |
| Commit response is ambiguous | Pending state remains | Retry the same commit key |
| Commit explicitly reports expiry | Pending state remains | Create one replacement session |
| Provider fails while detached | Reopen shows startup failure | Existing failure ownership applies |
| Application crashes | No immediate request is required | Server expiry reclaims sandbox |

No failure may erase typed text, expose an empty terminal, or commit the first
prompt twice.

## 10. Resource and security limits

- The lease duration is fixed by the server at two minutes.
- Clients cannot request a longer duration.
- Hidden preparations continue counting against sandbox quota.
- Ordinary session, child, orchestrator, and shared-project lists continue
  excluding preparations.
- Reuse never crosses authenticated principals or organizations.
- Lease and telemetry payloads contain no prompt, message, terminal, repository,
  credential, or browser data.
- Chromium remains absent until explicit browser intent.
- Provider deletion after expiry remains mandatory even if the renderer still
  holds a stale handle.

## 11. Telemetry

Add content-free renderer and control-plane events for:

- preparation acquired as new or reused;
- preparation detached;
- renewal attempted, succeeded, or failed by category;
- detached preparation reattached;
- preparation expired while attached or detached;
- commit recovered through fresh creation.

Measure:

- reopen-to-reattach latency;
- avoided duplicate sandbox count;
- detached grace duration before commit or expiry;
- renewal failure rate;
- replacement-after-expiry rate;
- provider cleanup delay after lease expiry.

Do not record compatibility-key values beyond existing opaque project and
session identifiers.

## 12. Testing

### 12.1 Renderer unit tests

- Opening creates one preparation.
- Close releases without deleting.
- Reopen within two minutes reuses the same session and create key.
- Reopen after expiry creates a replacement.
- Meaningful activity renews an attached preparation.
- An untouched open composer stops renewing after two minutes.
- Harness or provider change invalidates and deletes the old preparation.
- Multiple compatible mounts share one handle and attachment count.
- Commit prevents unmount cleanup from releasing the handle.
- Explicit expiry during commit preserves the prompt and creates one
  replacement.
- Ambiguous commit retries use the same idempotency key.

Use fake timers and deterministic server deadlines. No unit test waits for real
minutes.

### 12.2 Store and HTTP tests

- Renew updates both expiry columns to the same database timestamp.
- Renew rejects expired, committed, deleted, terminated, and foreign sessions.
- Concurrent renew and commit produce one committed session or one renewed
  preparation, never mixed state.
- Commit exposes the stable expired error without leaking foreign existence.
- Expiry reconciliation still requests deletion.
- Repeated renewal does not create sessions, sandboxes, turns, or commands.
- OpenAPI and generated clients remain in sync.

### 12.3 Local lifecycle tests

1. Open New Task and record the hidden session identifier.
2. Close and reopen after 30 seconds.
3. Assert the identifier and provider environment are unchanged.
4. Commit and assert the first prompt appears once.
5. Repeat, wait past the lease, and assert a new identifier is used.
6. Keep an active composer open past the original deadline and assert renewal.
7. Keep an idle composer open past the deadline and assert provider cleanup.
8. Change harness and provider and assert immediate replacement.
9. Stop the renderer process and assert server cleanup without a close request.

Run the existing cold-start transport, restart, replacement, pause and resume,
checkout, failure, race, frontend, and packaged desktop checks afterward.

## 13. Acceptance criteria

1. Closing an unsubmitted composer does not immediately delete its preparation.
2. With healthy control-plane transport, reopening a compatible composer within
   two minutes of close reuses the same session identifier and provider
   environment.
3. Reattach completes without restarting checkout or the harness.
4. Meaningful activity can keep a composer valid beyond its original creation
   deadline.
5. An untouched open composer stops consuming compute after the server lease
   and provider deletion complete.
6. Reopening after expiry creates fresh cold compute and never resurrects the
   expired sandbox.
7. A late submit preserves the full prompt and results in exactly one visible
   session and first durable turn.
8. Changing project, harness, provider, or provider connection never reuses an
   incompatible preparation.
9. A crash with no cleanup request leaves no provider resource beyond the
   two-minute lease, reconcile delay, and measured provider deletion time.
10. No warm pool, shared standby worker, or pre-created sandbox is introduced.
11. Hidden preparations remain absent from every ordinary session list.
12. All focused tests and the existing Cloud cold-start regression matrix pass.

## 14. Implementation slices

### Slice 1: server lease renewal

- Add the renewal store transaction and HTTP endpoint.
- Return authoritative expiry metadata from prepare and renew.
- Add stable expired and committed error codes.
- Regenerate the Cloud API contract and clients.

### Slice 2: renderer preparation registry

- Move preparation ownership above `TaskComposer`.
- Add compatibility keys, attachment counts, activity tracking, and timers.
- Replace unmount deletion with release and grace.
- Keep immediate invalidation for incompatible settings.

### Slice 3: late-submit recovery

- Retry ambiguous commit responses with the same key.
- Replace only after explicit expiry.
- Preserve the pending route and complete prompt during replacement.

### Slice 4: lifecycle validation

- Add unit, store, HTTP, and local lifecycle coverage.
- Run the existing cold-start regression matrix.
- Measure reattach latency and provider cleanup delay.
- Verify no duplicate provider environment is created during compatible reopen.

## 15. Expected file surface

Control plane and storage:

- `cloud/internal/httpapi/resource_handlers.go`
- `cloud/internal/httpapi/server.go`
- `cloud/internal/postgres/project_session_store.go`
- `cloud/internal/postgres/sandbox_store.go`
- focused preparation handler and store tests

Contracts and clients:

- `contracts/cloud/openapi.yaml`
- `packages/cloud-client/src/schema.ts`
- `packages/cloud-client/src/client.ts`
- frontend Cloud client types and methods

Renderer:

- `frontend/src/renderer/lib/cloud-session-preparation.ts`
- a preparation registry module and focused tests
- `frontend/src/renderer/components/TaskComposer.tsx`
- focused composer tests

Validation:

- `cloud/scripts/test-cloud-local.sh`
- the Cloud cold-start validation report

## 16. Rollout and completion record

Land the server lease contract before enabling renderer retention. A renderer
without registry support keeps the current immediate-delete behavior. Once the
registry is enabled, monitor renewal failures, detached resource duration,
replacement rate, and duplicate sandbox attempts.

Implementation is complete only when the handoff includes:

- changed files grouped by slice;
- exact test commands and observed results;
- proof that compatible reopen keeps the same session and provider environment;
- proof that idle and crashed clients are reclaimed;
- proof that late submit preserves the prompt exactly once;
- provider cleanup timing;
- confirmation that no warm capacity or implicit browser startup was added.

The implementation and observed validation results are recorded in
`../reports/2026-09-22-cloud-cold-start-validation.md`, under the reconnect
grace follow-up.
