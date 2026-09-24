# Cloud session review remediation

Date: 2026-09-24
Target: PR #5543
Baseline: `9a92b900e9641bf75f1f7428edec26491017164d`
Status: implemented locally; automated checks pass; native desktop acceptance remains open

## Scope

Repair the twelve findings from the September 24 review. Preserve cold-start
allocation, early typing with delayed execution, two-minute preparation reuse,
and one shared remote browser. No warm pools, new provider integration, browser
engine relocation, or unrelated architectural rewrite.

The desktop remains a viewer and input client. Chromium and page execution stay
in the session machine. Terminal connectivity is not proof that checkout or the
coding harness is ready.

## Delivery and preparation

### R1: Keep early messages deliverable

Accept startup-composer submissions after terminal readiness. Flush messages in
both accepted and ready states, preserving client sequence and retry ordering.
Keep the composer visible while any message lacks a server acknowledgement,
including saving, sending, and failed messages. Do not clear evidence of an
unsent instruction when the terminal first paints.

Acceptance: submit a draft spanning readiness; retry a failed message after
readiness; delay its response. Each instruction is sent once per attempt and
remains visible until acknowledged.

### R2: Keep startup instructions durable

A reserved terminal must not qualify for direct prompt delivery. Route messages
accepted before the current worker is execution-ready through the durable turn
queue. An early terminal attachment must not promote that readiness. Preserve
the direct terminal path for a genuinely ready, idle harness.

Readiness must be fenced to the current worker epoch. A restarted worker must
not inherit the previous worker's readiness. Acknowledging input held only in
process memory does not count as delivery.

Acceptance: accept the initial instruction and a follow-up during checkout,
restart before execution, and recover both in order. Cover reserved-terminal
attachment and the ready-terminal fast path separately.

### R3: Persist cleanup obligations

Record an outstanding creation obligation before calling the provider. A late
resource identifier must be durably retained with deletion intent when its
claim or preparation is no longer current. Use a bounded context independent
of operation cancellation for recording the outcome. Deletion failures remain
retryable; they must not be reduced to log messages.

Do not finalize provider absence while creation has an unresolved outcome.
Recovery must cover a process exit between provider creation and recording its
identifier, using the existing provider session lookup. Never attach a stale
resource to a replacement preparation or overwrite its active resource.

An unresolved provider outcome blocks replacement and final deletion until the
resource can be located. A capacity rejection without a resource identifier is
definitive and can release the obligation. Other ambiguous failures remain
pending; a timeout alone cannot prove that creation stopped.

Acceptance: expire preparation during creation, lose the lease, cancel the
request context, fail the first deletion, and reconcile again. The late
resource is eventually absent and the replacement session is untouched. Test
concurrent deletion observing absence before creation returns.

### R6: Preserve ownership across quick reopen

Recheck local attachment ownership after awaiting initial creation before
sending detach. Reopening while creation is pending retains the same request
and machine. Explicit invalidation still detaches an obsolete configuration.

Acceptance: open, close, reopen before the create response. Observe one create,
no detach of the active attachment, and successful commit. Retain tests for
configuration changes, grace expiry, and simultaneous composers.

## Browser transport and runtime

### R4: Separate response progress from event consumption

The CDP socket reader must never wait indefinitely for an event consumer that
is itself awaiting a command response. Bound event buffering. On overflow,
close the connection with an explicit error so reconnect can recover rather
than silently lose arbitrary events. Give every command a deadline, including
commands using a session-lifetime context. Only the event producer closes its
output channel.

Acceptance: a real loopback WebSocket emits more than the old 16-event capacity
before replying; the command completes. Saturation has a bounded failure
outcome. Exercise concurrent close and event delivery with the race detector.

### R5: Fence and deduplicate controls

Validate stream epoch and positive input sequence before mutating browser state
or acquiring control. Track the highest processed sequence and a bounded cache
of acknowledgement/rejection outcomes per viewer connection. Duplicate controls
return their prior outcome without repeating side effects. Evicted sequences
are rejected. Old-epoch controls never operate on a reconnected page.

Keep connection-management messages explicitly separate from page mutations.
Do not log clipboard text or key payloads. Preserve read-only enforcement at
both the relay and worker boundary.

Epochs must fit JavaScript's safe integer range. Use microseconds plus a local
monotonic counter rather than serializing nanoseconds as a JSON number.

Acceptance: duplicate text input and clicks execute once; stale epoch, zero
sequence, replay after cache eviction, and reconnect-buffered input are rejected.

### R9 and R10: Initialize once and bound execution

Synchronize runtime directory/configuration initialization. All concurrent first
commands use one socket root. Failed initialization can retry; successful
configuration is not rewritten by subsequent commands. Normalize the command
timeout before constructing the process runner.

Acceptance: concurrent first commands share configuration under `go test -race`;
default and explicit timeouts reach the runner; cancellation stops a blocked
process.

## Browser interaction

### R7: Route editing input deliberately

Forward supported selection, undo/redo, and navigation shortcuts while leaving
application/window shortcuts local. Map platform editing modifiers to the
remote browser's conventions. Paste plain text from an explicit paste event,
with a byte limit matching the transport. Prevent hidden local text from
accumulating. Do not add automatic clipboard polling or continuous sync.

The worker includes virtual key codes for editing shortcuts. Paste is limited
to 8 KiB of UTF-8 text. Copying a remote selection into the local clipboard is
not added by this remediation and must not be advertised as supported.

Selection/copy behavior must be tested in the real desktop before claiming
cross-platform clipboard support. If copy requires an additional command or
permission, document that boundary rather than swallowing the shortcut.

Acceptance: supported shortcut dispatch, bounded plain-text paste, rejected
oversized paste, read-only behavior, and composition input. Manually verify
selection and paste in the actual desktop against the remote page.

### R8: Gate coordinates on the painted viewport

A resize acknowledgement alone does not enable pointer input. Require a frame
for the acknowledged viewport to be painted. Tie the acknowledgement to the
latest requested viewport and a frame boundary; ignore obsolete acknowledgements
and stale frames. Repeated or rapidly reversed resizes must remain fenced.

Acceptance: resize from 800x600 to 1000x800, acknowledge, delay the resized
frame, and attempt a click. No click is sent until that frame paints. Cover
rapid consecutive resizes and disconnect while resizing.

## Retry and API compatibility

### R11: Recover local delegation reservations

Associate the idempotency reservation with a durable spawn identity before
external workspace/runtime side effects. Repeated requests reconcile that
identity instead of spawning another worker. A confirmed pre-spawn failure is
retryable; an uncertain spawn outcome must not be blindly cleared.

Keep fingerprint conflict detection. A transient finalization failure or daemon
restart must not leave a recoverable request permanently in progress. Use the
existing session lifecycle and storage transaction boundaries; do not introduce
a second runtime launcher in the service.

Commit the worker seed and reservation association in one SQLite transaction.
The follow-up review found that identity-only replay was insufficient: existing
session recovery cannot restore an incomplete seed. Migration 0157 adds startup
checkpoints. Only completed startup replays successfully; an unclaimed seed can
continue through the manager, while an uncertain external startup requires
inspection. See [follow-up specification](2026-09-24-cloud-review-followup.md).

Mark newly inserted reservations as recoverable. Existing completed reservations
continue to replay. An older pending reservation without an associated worker
returns `TASK_DELEGATION_RECOVERY_REQUIRED`: its previous spawn outcome is
unknown, so starting another worker automatically is unsafe.

Acceptance: reserve then restart; spawn then fail finalization; retry a confirmed
pre-spawn failure; repeat concurrently. Each key has at most one worker and a
recoverable result.

### R12: Preserve the existing message payload

Continue accepting `{text}` on the existing v1 endpoint. An omitted
`clientSequence` maps to absent metadata. If present, require a positive integer.
Update source contract and generated client types together.

Acceptance: legacy and sequenced requests succeed; explicit zero, negative,
fractional, and malformed sequences fail validation.

Explicit JSON null also fails validation; omission remains compatible.

## Storage and rollout

- SQLite migration 0156 distinguishes new recoverable delegations from older
  ambiguous pending rows. Migration 0157 separates seeded identity, claimed
  startup, and completed startup. Regenerate sqlc output from the query source.
- Postgres migration 00044 records tenant-scoped creation obligations. Migration
  00045 indexes current-worker readiness lookups without scanning the transcript.
- Deploy migrations with the control plane. Update desktop and worker together
  for epoch/sequence enforcement. Existing workers must be replaced or restarted
  from the updated image before their browser behavior can be claimed fixed.
- No live worker image or provider template is changed merely by editing this
  checkout. Real desktop, remote-provider, and native-platform checks remain
  separate acceptance evidence.

## Scoped consolidation

Share helpers only across genuine repeated call sites touched by these fixes.
Prioritize viewer state defaults, preparation lease adoption, and repeated
control outcomes. Preserve validation at separate trust boundaries. Remove
whitespace-only review noise when it can be separated from behavior changes.

No LOC quota: measure net changes after tests. Generated artifacts and regression
coverage are not candidates for deletion merely to reduce the PR header.

## Implementation order and gates

1. Delivery/preparation: R1, R2, R6, R12.
2. Browser runtime: R4, R5, R9, R10.
3. Interaction: R7, R8, then real desktop verification.
4. Durable recovery: R3 and R11, including storage failure injection.
5. Consolidation, full affected suites, review of the final diff.

Every R-number is a separate acceptance gate. A passing existing suite is not
proof of a newly identified interleaving; add the reproducer first. Keep unmet
gates visible in the implementation report.

Verification commands, from their respective module directories:

- Frontend: focused Vitest tests, complete renderer test suite, typecheck, build.
- Cloud: focused tests, `go test ./...`, `go test -race ./...`, `go build ./...`,
  and `go vet ./...`; database tests need their real test database.
- Backend: focused delegation/storage tests, `go test ./...`, `go test -race ./...`,
  `go build ./...`, and `go vet ./...`.
- Contracts: regenerate changed types and verify no remaining drift.
- Final patch: `git diff --check`; real app interaction evidence for UI changes.

Do not treat unavailable native runners, provider credentials, database tests,
or desktop evidence as passed. No push or PR update is part of local validation.

## Design review

The design preserves the current module boundaries. Readiness is not inferred
from terminal transport, cleanup is durable, CDP overload terminates explicitly,
and viewport safety depends on painting rather than receiving an acknowledgement.
The two storage recovery changes need transaction-level tests in addition to
service fakes. Scope and completion claims must follow observed evidence.
