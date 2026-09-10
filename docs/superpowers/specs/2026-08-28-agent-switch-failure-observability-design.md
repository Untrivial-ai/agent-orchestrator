# Agent Switching Failure-Only Observability Design

Status: approved design

Date: 2026-08-28

Assessed baseline: main at 7ea6f221d

Rebase note: the six intervening main commits did not change agent-switch
semantics or existing Sentry adapters. They did extend daemon-owned settings and
startup wiring; the consent-authority section below explicitly accounts for
that boundary.

## Goal

Add end-to-end failure observability around asynchronous agent switching while
sending no successful switch reports to Sentry.

The design covers the complete path from request admission through the
daemon-owned saga, TUI and Chat runtimes, SQLite ownership transitions,
lifecycle hooks, handoff artifacts, compensation, shutdown and restart
reconciliation, frontend visibility, and delivery of the failure report itself.

A developer must be able to answer these questions from one failure event:

- At which exact boundary did the switch fail?
- Which durable phase was committed?
- Which agent probably owns the session?
- Did the external side effect occur, fail, or remain unknown?
- Did compensation succeed?
- Is the user blocked behind a retained operation gate?
- Was the failure produced live, during startup reconciliation, or during an
  explicit recovery attempt?
- Which code location classified the failure?

## Current Behavior

The daemon initializes Sentry in
backend/internal/observe/sentryobs/sentryobs.go. It currently captures HTTP 5xx
responses, except retryable 503 responses, and HTTP panics. The renderer has its
own Electron Sentry integration for frontend and API failures.

Accepted switches return HTTP 202 after the preparing_handoff row is durable.
Execution then moves to a daemon-owned background worker in
backend/internal/session_manager/agent_switching.go. Worker errors, worker
panics, recovery failures, and startup reconciliation failures do not pass
through HTTP middleware. They are currently logged locally and therefore remain
invisible to developers when they happen on a user's machine.

Frontend reporting cannot close this gap. The renderer sees only the public
switch projection and may be absent, reloading, disconnected, or running in a
different window. CLI and headless clients create the same daemon-side work
without a renderer.

## Scope

This design includes:

- Unexpected admission failures after resolving whether a saga exists.
- Every durable failed switch transition.
- Every first durable nonterminal recovery marker.
- Live TUI and Chat execution.
- Runtime, controller, lifecycle, native-session, artifact, and SQLite
  boundaries.
- Compensation and ownership ambiguity.
- Worker and recovery panics.
- Startup and explicit reconciliation.
- Post-terminal maintenance failures.
- Renderer transport, query, and presentation failures that prevent durable
  switch state from being observed.
- Durable, consent-aware, failure-only delivery to Sentry.
- Alert grouping, severity, privacy, and fault-injection verification.

## Non-Goals

The design does not add:

- Successful switch analytics.
- Sentry tracing or performance spans.
- Global Sentry breadcrumbs.
- Durable journaling of successful decisions.
- Prompts, handoff contents, transcripts, terminal output, or provider payloads
  in observability.
- A generic repository-wide outbox framework.
- Mobile observability.
- New switch cancellation semantics.
- A broader switch UX redesign beyond recovery visibility.

## Definition of Complete Coverage

A literal list of raw error strings cannot remain complete as adapters evolve.
Coverage is instead defined by the Cartesian product:

~~~text
phase
× subsystem
× call outcome
× durable state
× real ownership
× compensation result
× user impact
× reporting action
~~~

Every fallible or side-effecting boundary must classify into one of these call
outcomes:

~~~text
ok
expected_rejection
no_effect_failure
committed_response_lost
effect_unknown
stale
timed_out
cancelled
panic
cleanup_failed
~~~

The important ownership classifications are:

~~~text
source
none
target
ambiguous
~~~

The compensation classifications are:

~~~text
not_needed
succeeded
failed
uncertain
~~~

## Durable State Model

The existing durable states remain authoritative:

~~~text
preparing_handoff
→ stopping_source
→ source_stopped
→ starting_target
→ target_ready
→ delivering_context
→ completed
~~~

Any nonterminal phase may transition to failed. These error codes can mark a
nonterminal recovery condition:

~~~text
source_stop_unconfirmed
source_restore_unconfirmed
target_start_unconfirmed
~~~

source_stop_unconfirmed, source_restore_unconfirmed, and
target_start_unconfirmed are retained nonterminal recovery conditions. They keep
the operation gate because ownership or liveness is not safe enough to permit a
new switch. They are forbidden on failed rows. A terminal failure reached after
the uncertainty is resolved uses a separate terminal error code describing the
proven outcome. Consumers still use report_kind and state with error_code;
error_code alone is not enough.

Delivery uncertainty is terminal because replay after an attempted continuation
could duplicate a user turn:

~~~text
delivery_unconfirmed
~~~

The switch row and session row remain the ownership authority. Sentry never
decides, repairs, or mutates saga state.

## Report Classes

The system emits only the following classes:

| Report kind | Meaning |
| --- | --- |
| terminal_failure | A failed-state CAS won |
| recovery_required | A first nonterminal ambiguity marker was committed |
| panic | A live or recovery worker panicked |
| recovery_attempt_failed | Reconciliation failed without producing a new durable classification |
| maintenance_failure | Post-terminal cleanup failed |
| daemon_lifecycle_failure | One aggregate switch-worker shutdown failure for a daemon run |
| visibility_failure | The renderer could not observe or present durable daemon state |

Expected validation, conflicts, stale callbacks, idempotent replay, successful
fallback, successful read-back recovery, and the successful completion itself
produce no event. A later proven post-terminal maintenance failure is a separate
failure and may report.

Operational classes use a separate fault_code rather than inventing a switch
error_code:

~~~text
worker_panic
recovery_unresolved
terminal_cleanup_failed
shutdown_workers_timed_out
~~~

Semantic terminal/recovery reports set fault_code=not_applicable. Operational
and daemon reports set error_code=not_applicable unless a real durable switch
error code also applies. The applicability matrix is enforced by the taxonomy
test.

## Alternatives Considered

### Direct Sentry calls at every error branch

Rejected because the same root failure would be reported by adapters, storage,
the Manager defer, reconciliation, HTTP middleware, and the renderer. Direct
capture also creates a crash window between the durable switch mutation and
Sentry capture, and it risks exporting raw errors containing paths or provider
identifiers.

### Full durable decision journal

Rejected because persisting every successful branch would create unnecessary
schema, storage, retention, privacy, and migration complexity. The product
requires failure observability, not event sourcing.

### Reuse change_log or the existing telemetry table

Rejected because change_log is trigger-owned UI invalidation and not an
acknowledged delivery queue. The existing telemetry sink is a best-effort
analytics path with arbitrary payloads and no transaction coupling, lease,
dedupe, or remote acknowledgement.

### Renderer or CLI as the primary reporter

Rejected because async outcomes can occur while no client is connected.
Reloads, multiple windows, CLI timeouts, and headless operation would also
create missing and duplicate reports.

## Selected Architecture

The selected design has six components:

1. A Session Manager-owned typed failure classifier.
2. A safe in-memory flight recorder for the active execution.
3. An internal durable failure_point on the switch row.
4. A dedicated agent-switch failure outbox in SQLite.
5. One daemon-lifetime dispatcher with a provider-neutral observer port.
6. A Sentry adapter and a separate frontend visibility reporter.

The data flow is:

~~~text
Manager reaches a semantic decision
→ build typed safe fault
→ atomically update switch state and enqueue fault
→ wake or poll durable outbox
→ provider-neutral observer
→ Sentry
~~~

If the switch completes, the flight recorder is discarded and no outbox row is
created.

## Reporting Ownership

The Session Manager owns switch execution reports because it alone knows:

- Whether an external side effect may have happened.
- Whether a SQLite commit was applied.
- Which generation owns the session.
- Whether cleanup made rollback safe.
- Whether source restoration succeeded.
- Whether acknowledgement beat the failure race.

Runtime, Chat, lifecycle, filesystem, native-session, storage, and provider
adapters return typed outcomes or errors. They do not call Sentry for a switch
execution failure.

Before durable saga admission, unexpected failures remain owned by existing HTTP
5xx reporting. After admission, the saga owns reporting, even if a synchronous
error is returned to the original HTTP request.

For an ambiguous create:

~~~text
exact saga row found
→ saga/outbox owns reporting

no row conclusively found
→ HTTP owns reporting

read-back also unavailable
→ retain any admission gate, write privacy-safe local diagnostics,
  and let the existing generic HTTP durability-error surface own the 5xx
~~~

The ambiguous case does not directly emit a semantic switch report because the
create commit may have applied and a later outbox row could otherwise duplicate
it. The generic HTTP event is grouped as an admission/storage availability
incident, not as a durable switch outcome.

Add a small backend observability ownership enum with values http and
agent_switch_saga plus an OwnedError wrapper implementing Error, Unwrap, and
ObservabilityOwner. Session Manager wraps every synchronous post-admission
error with agent_switch_saga; wrapping with %w must preserve errors.As. The
controller error translator copies only the safe owner enum into the API error
envelope's optional reporting_owner field and into request-local captured-error
metadata. HTTP Sentry middleware checks errors.As/reporting_owner and skips
agent_switch_saga while continuing to render the normal status and domain code.

Generated API types carry reporting_owner to the renderer. The generic renderer
API exception helper suppresses capture when it equals agent_switch_saga, while
the UI still presents the failure. Pre-admission unexpected failures remain
http-owned. Contract tests cover direct, singly wrapped, multiply wrapped, and
serialized/deserialized ownership so saga, HTTP, and renderer cannot all report
the same failure.

## Flight Recorder

Each admitted worker owns an in-memory enum-only record:

~~~text
failure_point
last_durable_phase
call_outcome
ownership
compensation
user_impact
source_stop_confirmed
target_owner_committed
target_ownership_ambiguous
gate_retained
execution
~~~

The recorder is not a Sentry breadcrumb trail. It never contains arbitrary
strings, errors, prompts, paths, identifiers, terminal output, or provider
content.

Optional degradations may update it, but the data is discarded if fallback
succeeds and the switch completes.

## Failure Point

Add failure_point to agent_switches through a new migration. Existing migrations
must not be modified.

The field is:

- A stable internal enum.
- Empty during ordinary progress.
- Set when a terminal failure or recovery marker is classified.
- Updated only with a new semantic failure classification.
- Omitted from the public API projection initially.
- Persisted locally even when remote telemetry is disabled.

error_code remains the broad user-facing and recovery-facing code.
failure_point identifies the exact internal boundary.

Examples:

~~~text
error_code=target_start_unconfirmed
failure_point=target_runtime_create

error_code=failed_post_stop
failure_point=final_artifact_publish

error_code=delivery_unconfirmed
failure_point=chat_target_ack_commit
~~~

## Failure Point Taxonomy

The initial stable vocabulary is:

### Admission

- admission_saga_create
- admission_commit_readback
- admission_chat_handoff_arm
- worker_start_refused

### Preflight and handoff

- source_native_preserve
- target_preflight
- target_resume_lookup
- handoff_directory_prepare
- handoff_collection
- handoff_settlement
- decision_input_close
- source_handoff_interrupt
- chat_source_quiesce
- target_launch_gate_prepare
- stopping_source_commit

### Source stop

- source_runtime_destroy
- source_runtime_probe
- source_controller_stop
- source_controller_drain
- source_stop_commit
- source_stop_readback

### Context and artifact

- source_metadata_refresh
- semantic_artifact_verify
- source_transcript_capture
- continuation_build
- final_artifact_publish
- final_artifact_verify
- final_artifact_commit
- target_prompt_prepare
- target_workspace_prepare

### Target start: common and TUI

- target_native_prepare
- target_native_commit
- target_runtime_create
- target_handle_commit
- target_generation_probe
- target_native_identity_wait
- target_activation_commit
- target_activation_readback

### Target start: Chat

- chat_provider_start
- chat_provider_resume
- chat_native_identity_commit
- chat_provider_boundary_commit
- chat_target_activation_commit
- chat_target_activation_readback
- chat_controller_publish

### Delivery

- delivery_open_commit
- tui_target_hook_wait
- tui_target_ack_commit
- chat_continuation_relay
- chat_target_ack_commit
- completion_commit

### Compensation and recovery

- target_runtime_cleanup
- target_workspace_cleanup
- source_runtime_restore
- source_controller_restore
- recovery_session_load
- recovery_runtime_probe
- recovery_native_identity
- recovery_artifact_verify
- recovery_activation
- recovery_settlement
- recovery_existing_marker

### Process, maintenance, and visibility

- live_worker_panic
- recovery_worker_panic
- shutdown_worker_timeout
- terminal_artifact_cleanup
- visibility_transport
- visibility_query
- visibility_presentation
- outbox_delivery
- classification_unknown

A table-driven completeness test must require every enum to map to a subsystem,
valid phases, valid error/fault codes, default severity, a report kind or
explicit local_only action, title, and runbook entry. outbox_delivery maps to
local_only and no remote report kind, preventing recursive delivery incidents
while still making the branch explicit. classification_unknown is the safe
local-only sentinel for invalid/panicking observability classification and can
never be serialized to Sentry.

## SQLite Policy

Add an agent_switch_failure_policy singleton table:

| Column | Purpose |
| --- | --- |
| singleton | Primary key constrained to 1 |
| enabled | Whether new switch faults may enter the remote-delivery outbox |
| consent_generation | Exact desktop policy generation or headless boot token |
| destination_fingerprint | Non-secret hash of the validated Sentry destination |
| updated_at | Policy synchronization time |

The singleton is seeded disabled. On every startup, before reconciliation or
dispatcher claims, the daemon closes the in-memory gate and transactionally
forces the SQLite policy disabled without inventing or advancing the mirrored
consent_generation. It then synchronizes an exact authority token. If
synchronization fails, startup fails closed for switch reporting: saga recovery
may proceed only after the forced-disabled transaction succeeds, and the
dispatcher does not claim rows. A persisted stale enabled value can therefore
never authorize a new payload or delivery after restart. When the authoritative
setting is disabled, startup also purges all outbox payloads before
reconciliation; receipts remain under their incident-lifetime rule.

enabled is true only when:

~~~text
valid telemetry events consent token
AND a Sentry DSN is configured and validated
AND ao.agent_switch.failure is not kill-switched
~~~

destination_fingerprint is SHA-256 over the validated normalized scheme, host,
effective port, base path, numeric project ID, and public key. It contains no DSN
secret and binds enrolled payloads to the provider project that was authorized
at classification time.

### Authoritative consent and cross-process protocol

The durable desktop user choice lives in AO_DATA_DIR/telemetry_policy.json,
which resolves under ~/.ao. The file contains only schema_version,
events_enabled, consent_generation, and updated_at and is written atomically
with mode 0600. Electron main is the sole file/generation writer. On the first
packaged launch, before any Sentry client is initialized, main materializes the
current packaged default; thereafter the stored user choice wins. Environment
controls remain hard vetoes and can never turn a stored off choice on.
An unreadable, malformed, unsupported-version, or permission-unsafe existing
file fails closed and is not silently replaced with an enabled default.

"Written atomically" here also means crash-durable before acknowledgement:
main writes a same-directory non-symlink temporary file, flushes file contents
and metadata (fsync or FlushFileBuffers), performs an atomic replace, and flushes
the parent directory where the platform supports it. Windows uses a replace/
write-through primitive with equivalent durability. Only after those operations
succeed may main publish or acknowledge the new generation. Failure leaves both
desktop/daemon gates closed and never rematerializes an enabled default over an
uncertain existing file.

This narrow supervisor bootstrap file is intentional even though ordinary user
preferences on current main are daemon-owned in app_settings: Electron must
decide whether any desktop Sentry transport may exist before the daemon and its
SQLite service are available. Renderer settings use typed IPC to main for this
one cross-process consent control; saga logic and atomic failure enrollment stay
daemon-owned. The SQLite singleton is a transactional mirror, not a second
authority.

For a desktop-supervised run, the stored choice is necessary but not sufficient
for a surface:

~~~text
desktop main/renderer = stored events_enabled
                        AND AO_TELEMETRY_RENDERER is not off
                        AND ao.agent_switch.visibility_failure is not kill-switched

daemon switch stream = stored events_enabled
                       AND AO_TELEMETRY_EVENTS is on
                       AND DSN is configured
                       AND stream kill switch is absent
~~~

A directly launched headless daemon never writes the desktop policy file. Its
exact truth table is:

| Policy file | AO_TELEMETRY_EVENTS | Effective headless choice |
| --- | --- | --- |
| valid off | any value | off |
| valid on | explicit on | on with the file generation |
| valid on | absent or off | off |
| missing | explicit on | on for this boot only, with a random headless boot token |
| missing | absent or off | off |
| malformed, unsafe, or unsupported | any value | off |

Thus an explicit headless environment opt-in is an alternative consent source
only when no stored desktop opt-out exists. It is process-scoped and does not
become a competing durable generation writer.

Startup ordering is:

1. Electron main resolves AO_DATA_DIR and reads or materializes the policy
   before initializing the desktop Sentry transport or renderer capture intake.
2. Renderer bootstrap carries events_enabled and consent_generation. Renderers
   never own a network Sentry client or DSN; when enabled they send only typed,
   pre-sanitized capture requests to main through preload IPC.
3. The spawned daemon receives the data directory and config vetoes, forces its
   SQLite mirror disabled, reads the same file itself, and then acknowledges the
   exact generation/value it synchronized.
4. Main enables the sole desktop Sentry transport and renderer IPC intake only
   after the durable policy is known. Daemon dispatch begins only after its
   matching-generation acknowledgement.

Live changes use two loopback-only internal commands that are not served by the
LAN listener. prepare-disable can only close the daemon gate; it cannot enable
reporting or change durable policy. apply-policy treats its body as a hint: the
daemon must re-read and validate telemetry_policy.json and applies only the exact
generation/value found there. It rejects a missing, malformed, stale, or
mismatched file. This is required because the primary loopback listener is
deliberately unauthenticated; a local request body alone can never enable
reporting.

The daemon also watches the authority independently: it performs a synchronous
file re-read before every claim and again at delivery-gate entry, plus a
one-second fail-closed polling loop to cancel an already registered call when an
off/new generation appears. Read, parse, permission, generation, or headless
truth-table mismatch closes the gate. This lets a current daemon discover an off
file even if prepare-disable was missed or main crashed. The file does not revoke
a request already accepted by the provider, and a possibly live daemon still
must acknowledge before main reports opt-out complete.

Main discards renderer capture requests with a stale or disabled generation.
Renderer acknowledgement is useful for clearing local queues but is not part of
the network-silence guarantee because renderers have no sender. Main waits for
its own transport and daemon acknowledgement before presenting a live change as
fully applied. Stale generations are rejected everywhere.

If the daemon is absent or unresponsive, main still disables its own surfaces,
persists the off generation for future processes, and records "daemon cleanup
pending" while retrying. It does not claim full application while a possibly
live daemon has not acknowledged; the next daemon reads the durable off
generation before initialization.
Destroyed or unresponsive renderers cannot send and require no acknowledgement,
and a newly created renderer receives only the latest bootstrap. Enabling is
blocked until all pending disable or purge work for older generations is
complete. The current eager initMainSentry call before bootstrap must move behind
this protocol.

The daemon also owns a process-wide delivery gate containing enabled,
consent_generation, a separate in-memory delivery_epoch, and the set of bounded
calls currently in flight. Starting a provider call atomically verifies the
authority token and epoch and registers that call under the gate. Closing the
gate advances delivery_epoch only; no daemon path invents a desktop
consent_generation. This closes the check-then-send race.

Opt-out performs this ordered protocol:

1. Main closes its desktop capture/network gate, advances its delivery epoch,
   cancels and awaits its bounded in-flight sends, and rejects new renderer
   capture requests.
2. Main calls prepare-disable. The daemon closes its gate, advances its separate
   delivery_epoch, cancels and awaits registered bounded calls, and acknowledges.
   If a possibly live daemon cannot acknowledge, opt-out remains visibly pending
   rather than claiming completion, but step 3 still protects future processes.
3. Main atomically writes events_enabled=false with exactly one new desktop
   consent_generation. This file replace is the durable opt-out linearization
   point for a fully acknowledged run; in a pending run it is the fail-closed
   state the current/future daemon must apply.
4. Main calls apply-policy with that generation. The daemon re-reads the file
   and, in one SQLite transaction, mirrors the exact generation/value and purges
   every outbox payload row, including pending, leased, delivered, and discarded
   rows.
5. Main deletes desktop cached envelopes, clears renderer-local capture queues,
   broadcasts the disabled generation, and only then returns
   opt-out completion to the user.

SQLite single-writer ordering guarantees that a concurrent failure either
inserts before the purge or observes disabled policy/generation and inserts no
remote payload. Each dispatcher claim carries the consent generation and
delivery epoch and rechecks both
through the delivery gate immediately before Observe. The gate owns the observer
invocation, so a registered-but-not-yet-sent call is cancellable and included in
the opt-out wait. A request already accepted by Sentry before cancellation
cannot be revoked; the product guarantee is that no provider call begins after
opt-out completes and no queued payload survives opt-out.

If either process crashes partway through opt-out, the next startup remains fail
closed until the authoritative policy is synchronized again. If SQLite purge or
Electron cache deletion fails, all delivery gates remain closed, the product
surfaces a local cleanup error, and cleanup retries on startup; failure never
rolls consent back to enabled.

Re-enabling verifies prior purge completion, atomically writes exactly one new
enabled desktop generation, asks the daemon to re-read and mirror it, and opens
daemon/main gates only after matching acknowledgements. It does not upload old
terminal failures. It
enrolls currently unresolved active recovery markers only when no retained
dedupe receipt proves that the incident was already enqueued under earlier
consent. A marker without a newer precise failure point uses
recovery_existing_marker.

## Failure Outbox Schema

Add agent_switch_failure_outbox in the same new migration.

| Column | Purpose |
| --- | --- |
| id | Stable random Sentry EventID |
| schema_version | Starts at 1 |
| envelope_encoding_version | Immutable deterministic wrapper version |
| dedupe_key | Unique local semantic incident key |
| destination_fingerprint | Destination authorized when the payload enrolled |
| switch_id | Local correlation only; never exported |
| report_kind | One of the approved report classes |
| scope | switch or daemon |
| failure_point | Stable internal boundary |
| classifier_callsite | Stable enum mapped to the classifying package/function |
| phase | Durable switch state at classification |
| error_code | Stable switch-domain error code or not_applicable |
| fault_code | Stable operational code or not_applicable |
| execution | live, startup_reconcile, explicit_recovery, or daemon_shutdown |
| execution_attempt_id | Opaque local worker/recovery invocation ID; never exported |
| mode | tui, chat, or not_applicable |
| from_harness | Safe harness enum or not_applicable |
| target_harness | Safe harness enum or not_applicable |
| target_start_mode | pending, fresh, resumed, or not_applicable |
| runtime_backend | Typed backend enum or not_applicable |
| call_outcome | Safe side-effect outcome enum |
| ownership | source, none, target, ambiguous, or not_applicable |
| compensation | not_needed, succeeded, failed, uncertain, or not_applicable |
| user_impact | Safe bounded impact enum |
| source_stop_confirmed | true, false, or not_applicable |
| target_owner_committed | true, false, or not_applicable |
| gate_retained | true, false, or not_applicable |
| requested_at | Local-only original switch request time; absent only for daemon scope |
| occurred_at | Fault classification time |
| sanitized_stack | Bounded safe capture frames where required |
| stack_fingerprint | Stable panic/capture-site grouping fact |
| canonical_event_json | Immutable canonical privacy-approved event bytes |
| expires_at | occurred_at plus the fixed seven-day payload TTL |
| available_at | Next eligible delivery time |
| attempt_count | Delivery attempt count |
| last_attempt_at | Last attempted delivery |
| lease_token | Current dispatcher claim |
| lease_consent_generation | Authority token captured by this claim |
| lease_delivery_epoch | In-process gate epoch captured by this claim |
| lease_expires_at | Crash-reclaim deadline |
| delivered_at | Successful observer delivery |
| discarded_at | Permanent quarantine or expiry |
| last_delivery_error_class | Safe transport enum, never provider text |

available_at is the only retry-scheduling field. Initial insertion sets it to
occurred_at; the claim predicate, retry update, and partial pending index all use
available_at. The index also includes occurred_at and only covers rows where
delivered_at and discarded_at are null.

After the core CAS wins, a panic-contained enrollment builder freezes release,
environment, channel, platform, OS, title, tags, safe context, sanitized frames,
EventID, and event timestamp into canonical_event_json inside the telemetry
savepoint. Construction uses a fixed typed struct, stable field order,
UTC/RFC3339 timestamps, and no maps with runtime-dependent ordering. A builder
error or panic becomes local_invariant_failed and cannot unwind the core CAS.

The store validates the schema/envelope versions and a 60-KiB event bound, then
persists those exact bytes. Dispatch selects the immutable encoder named by
envelope_encoding_version and never reconstructs or enriches the event. Encoders
remain supported for at least the payload TTL plus one release overlap window;
cross-version golden fixtures make changing an old encoder a CI failure. The
deterministic wrapper is bounded to 4 KiB, so the complete final envelope is at
most 64 KiB. Row ID, event JSON EventID, and envelope-header EventID must match or
the row is quarantined without sending.

switch_id is nullable only for daemon-scoped events. When present, it is opaque
TEXT with no foreign key to agent_switches. It is retained locally for
correlation but excluded from the remote event. This lets a pending fault survive
session deletion without blocking that deletion or cascading away the fault.
Daemon-scoped rows use an opaque local daemon_run_id in the dedupe key; it is
also excluded remotely. Non-applicable mode, phase, harness, start-mode, and
ownership fields use explicit not_applicable enum values instead of ambiguous
nulls. The table contains no user-authored content.

Every outbox payload has a hard seven-day TTL represented by expires_at. The
dispatcher never claims an expired pending row; maintenance atomically marks it
expired and removes all payload rows older than the TTL, regardless of pending,
delivered, or discarded state. A device that reconnects after the TTL does not
send that old event.

Add a separate payload-free agent_switch_failure_receipts table keyed by
dedupe_key. A receipt records only the opaque dedupe key, nullable opaque
switch_id, report kind, durable state fingerprint, recorded_at, and nullable
retain_until. It contains no stack, tags, error text, or provider payload.
switch_id has an ON DELETE CASCADE foreign key to agent_switches; daemon-scoped
receipts leave it null. The transaction inserts the receipt only when reporting
is enabled and the corresponding outbox row is created. Opt-out deletes every
outbox payload but retains receipts, preventing a previously enrolled unresolved
incident from being uploaded again after opt-in.

Retention is state-based and explicit:

- terminal_failure, panic, maintenance_failure, and daemon_lifecycle_failure
  receipts set retain_until to recorded_at plus seven days.
- recovery_required and recovery_attempt_failed receipts keep retain_until null
  only while the exact state/error/failure-point fingerprint remains unresolved.
  The transaction that changes/resolves that fingerprint sets retain_until to
  resolution time plus seven days.
- cleanup deletes every receipt with retain_until <= now. Deleting a switch
  cascades all of its receipts immediately, covering every deletion path.

The outbox payload deliberately has no switch foreign key. Its own unique key is
sufficient after no emitter remains, so it intentionally survives
session/switch deletion and can deliver an already consented failure. It contains
no exported switch ID and is hard-deleted by expires_at. This behavior must be
disclosed in telemetry documentation. Only a genuinely unresolved incident may
retain a payload-free receipt without a fixed deadline.

visibility_failure is delivered best-effort by the consent-gated Electron main
Sentry surface and does not enter the daemon SQLite outbox. Renderer windows
signal main through typed IPC and never deliver this class independently.

## Atomic Store Contract

Use explicit transactional store operations instead of an SQL trigger. A
trigger can observe state and error_code, but it cannot accurately capture the
specific failure point, side-effect outcome, ownership, or compensation result.

The store contract is:

~~~text
apply switch CAS
├── zero rows changed
│   └── insert no fault
└── one row changed
    ├── ordinary progress or completed
    │   └── insert no fault
    └── failed transition or first recovery marker
        ├── always persist failure_point with the saga mutation
        ├── policy disabled
        │   └── insert no receipt or outbox payload
        └── policy enabled and receipt absent
            └── savepoint: panic-contained validate/serialize/insert
                ├── success → release savepoint and commit all
                └── telemetry-local failure → roll back savepoint and commit saga
~~~

Store validation requires:

- A transition to failed carries typed fault input for validation and
  conditional enrollment.
- The first installation of a recovery marker carries typed fault input for
  validation and conditional enrollment.
- Completed and ordinary progress transitions carry no fault.
- A non-winning CAS inserts no fault.
- A repeated marker inserts no receipt or fault.
- An enrollment candidate's failure_point and durable phase/code combination is
  valid; an invalid candidate becomes local_invariant_failed without vetoing the
  core transition.
- The policy check uses the same writer transaction, never a cached setting or
  read-pool result. The policy coordinator supplies its currently authorized
  consent_generation immediately before the transaction, and enrollment uses
  INSERT ... SELECT ... WHERE policy.enabled = 1 AND
  policy.consent_generation = ?. Zero inserted rows is expected when disabled
  or stale; the core saga mutation still commits.

The delivery failure path remains specialized through
FailAgentSwitchIfUnacknowledged. Its state mutation and outbox insert occur only
when changed is true. If acknowledgement wins, the failure CAS changes zero rows
and no event exists to retract later.

No network or Sentry call occurs under the SQLite write lock.

When policy is enabled, healthy-path receipt/outbox enrollment is transactionally
coupled to failure settlement, but observability is subordinate to product
safety. Core transition validation runs independently. Taxonomy validation and
canonical serialization return optional enrollment plus a local invariant
status; they never return an error that can veto the core mutation. Valid
enrollment runs inside a SQLite savepoint. An outbox-only shape, constraint, or
serialization failure rolls back that savepoint, emits a privacy-safe local
invariant log, and still commits the core saga mutation and the best valid local
failure_point. It does not use direct Sentry fallback. A database-wide I/O,
full-disk, or commit failure may still prevent the whole transaction from
committing; that is a core storage failure, and the Manager follows the existing
retained settlement path.

Before the saga UPDATE, a total non-panicking lookup normalizes an unknown or
invalid observational failure point to classification_unknown, which is accepted
by the local column but is never remotely eligible. All richer taxonomy checks,
stack handling, canonical serialization, and their panic recovery run only after
CoreChanged inside the telemetry savepoint.

The store result separates CoreChanged from EnrollmentStatus
(enrolled, disabled, stale_generation, deduped, or local_invariant_failed).
Callers use CoreChanged for saga behavior and may log EnrollmentStatus, but can
never change ownership/compensation based on it.

When policy is disabled, the failure_point and saga mutation commit without a
receipt or outbox row. Telemetry enrollment failures, remote observer failures,
and network failures never change runtime ownership or compensation. The design
accepts the narrow loss of a report when its own local schema/invariant is
broken rather than changing the user-visible switch outcome.

Migration work must use a new migration and ALTER TABLE ADD COLUMN for
agent_switches where SQLite permits it. If a rebuild is unavoidable, the new
migration must recreate the latest indexes and trigger-owned CDC triggers.
Store code never writes change_log manually. SQL changes are made in migrations
and sqlc query sources, followed by regeneration; generated sqlc files are not
edited by hand.

Panics that produce no new state, recovery_attempt_failed, and
maintenance_failure use a separate EnqueueAgentSwitchOperationalFault-style
store operation on a short detached context after reloading the best durable
switch fact. These events use the same typed schema, conditional policy check,
receipt, and unique key but are not claimed to be atomic with an earlier state
transition. recovery_attempt_failed has one report per unchanged unresolved
incident; repeated attempts with the same durable phase, marker, failure point,
and error code update local logs only. A changed durable classification creates
a new incident. If SQLite is unavailable or commit outcome is ambiguous, the
semantic switch report is not sent directly; only privacy-safe structured
logging is allowed.

The standalone enqueue rules are mandatory, not optional:

| Class | Store operation | Cardinality |
| --- | --- | --- |
| panic without a new durable classification | EnqueueAgentSwitchOperationalFault | Once per switch execution_attempt_id, panic failure point, phase, and fault code |
| recovery_attempt_failed | EnqueueAgentSwitchOperationalFault | Once per unchanged unresolved incident; repeated attempts are local-only |
| maintenance_failure | EnqueueAgentSwitchOperationalFault | Once per switch, cleanup failure point, and daemon run; only after terminal settlement |
| daemon_lifecycle_failure | EnqueueAgentSwitchDaemonFault | Once per daemon run and shutdown_worker_timeout |

Both operations run a single short transaction, consult the tx-bound policy,
insert the payload-free receipt and payload only when enabled, and return a
typed changed result. Operational callers never call the observer. A panic that
is already attached to a winning failed/marker CAS does not use the standalone
API. Post-admission arm and worker-start failures use the normal atomic
terminal/marker path. The daemon aggregate uses switch_id absent and explicit
not_applicable values for switch-only fields.

Switch-scoped standalone enqueue never trusts a snapshot reloaded before its
transaction. Receipt/payload insertion is an INSERT ... SELECT guarded by the
still-present switch_id plus the expected durable state fingerprint
(state, error_code, failure_point, and updated_at/revision). Deletion or a newer
state winning first changes zero rows and prohibits post-deletion enrollment. If
enqueue wins first, a later product-data deletion removes its receipt while the
already-consented payload follows the documented seven-day TTL.

## Local Dedupe and Remote Grouping

Dedupe keys are canonical per report kind; one generic formula would either
duplicate recovery across execution modes or collapse distinct cleanup runs:

~~~text
terminal_failure | recovery_required
  v1 | switch_id | report_kind | failure_point | phase | error_code

panic without a winning semantic CAS
  v1 | switch_id | panic | execution_attempt_id | failure_point | phase | fault_code

recovery_attempt_failed
  v1 | switch_id | recovery_attempt_failed | phase
     | current_marker_or_error | failure_point | fault_code

maintenance_failure
  v1 | switch_id | maintenance_failure | daemon_run_id
     | failure_point | fault_code

daemon_lifecycle_failure
  v1 | daemon_run_id | daemon_lifecycle_failure | failure_point | fault_code
~~~

execution_attempt_id and daemon_run_id are opaque local UUIDs and are never
exported. The stable classifier_callsite maps through the taxonomy to an exact
package and function, so every event identifies its classifying code location
even when a P2 report intentionally has no stack. It is a validated event field
but is omitted from keys where the semantic failure point already provides the
canonical incident boundary.

The Sentry issue fingerprint is:

~~~text
agent-switch
| v1
| report_kind
| mode
| phase
| failure_point
| error_or_fault_code
~~~

Harness direction, runtime, release, OS, ownership, and compensation remain
tags. This prevents a single bug from becoming separate issues for every
provider direction or release.

Panic grouping is:

~~~text
agent-switch
| panic
| mode
| phase
| failure_point
| top_in_app_function
~~~

Visibility grouping is:

~~~text
agent-switch-visibility
| v1
| visibility_failure_type
| frontend_operation
~~~

## Provider-Neutral Observer

Introduce a provider-neutral outbound contract conceptually equivalent to:

~~~go
type DeliveryOutcome string

const (
    DeliveryAccepted          DeliveryOutcome = "accepted"
    DeliveryTransientFailure  DeliveryOutcome = "transient_failure"
    DeliveryPermanentFailure  DeliveryOutcome = "permanent_failure"
    DeliveryPolicyCancelled   DeliveryOutcome = "policy_cancelled"
    DeliveryShutdownCancelled DeliveryOutcome = "shutdown_cancelled"
)

type DeliveryResult struct {
    Outcome        DeliveryOutcome
    Class          DeliveryErrorClass
    RetryNotBefore time.Time
    ThrottleScope  DeliveryThrottleScope // none, error_category, or all
}

type AgentSwitchFailureObserver interface {
    ObserveAgentSwitchFailure(
        context.Context,
        AgentSwitchFailureEvent,
    ) DeliveryResult
}
~~~

The contract is:

- Events are immutable and already privacy-allowlisted.
- The same EventID may be delivered more than once.
- DeliveryAccepted means the provider accepted the event within the adapter's
  bounded delivery contract.
- DeliveryTransientFailure retries.
- DeliveryPermanentFailure quarantines.
- Policy and shutdown cancellation are control outcomes, never provider errors.
- RetryNotBefore and ThrottleScope carry parsed provider throttling on transient
  and accepted responses; a zero deadline/scope means no provider throttle.
- The observer cannot read or mutate saga state.
- Session Manager, domain, and storage packages do not import Sentry.
- The daemon supplies a Sentry adapter, no-op adapter, or recording test adapter.

The existing global sentry.CaptureException plus Flush wrapper cannot implement
this acknowledgement contract because enqueueing into the SDK does not expose a
per-event HTTP result. The switch outbox therefore uses a dedicated synchronous,
bounded Sentry envelope sender with the same DSN and privacy construction. It
returns accepted only after a 2xx provider response. Network errors, timeouts,
HTTP 408, 429, and 5xx are transient; other 4xx responses are permanent payload
or authorization failures and quarantine. Losing the response after provider
acceptance remains ambiguous and retries the identical EventID.

The outbox path does not call global CaptureException or a shutdown-wide Flush.
A no-op observer is valid only when policy is disabled and the dispatcher is not
started; it must never acknowledge an existing row. Recording test observers
must explicitly return accepted, transient, permanent, policy-cancelled, or
shutdown-cancelled outcomes.

Add an agent_switch_failure_delivery_state singleton keyed by
destination_fingerprint with error_not_before and all_not_before. Claiming is
blocked by the applicable persisted deadline, so rate limits survive restart.
On a transient response, row available_at becomes the later of local jittered
backoff, RetryNotBefore, and the applicable provider throttle. On an accepted
2xx, acknowledgement and any X-Sentry-Rate-Limits throttle update commit
together. A 429 without a category-specific header uses all scope. Changing the
destination fingerprint quarantines old-destination payloads and starts a fresh
throttle state; it never migrates an enrolled event to a different project.

### Sentry envelope transport requirements

The Go outbox sender and Electron visibility sender share golden envelope and
DSN-validation fixtures. They must:

- Accept a standard Sentry DSN only after validating scheme, public key, host,
  optional base path, and numeric project ID; reject embedded secrets, query,
  fragment, malformed escaping, or missing components.
- Require HTTPS in production. Plain HTTP is allowed only for an explicitly
  enabled loopback test/development endpoint.
- Construct the envelope endpoint from the validated origin/base path and use
  the public-key X-Sentry-Auth form. EventID is exactly 32 lowercase hexadecimal
  characters and never changes on retry.
- Route envelope_encoding_version to its frozen deterministic header/item
  encoder. It wraps canonical_event_json with only the stored EventID, fixed
  event type, and byte length and adds no send-time timestamp, release, SDK
  context, or host facts; envelope bytes are identical across retries/upgrades.
- Disable redirects and cookies so authentication and payloads cannot move to a
  second origin.
- Use normal OS certificate verification and proxy discovery. TLS protects the
  payload through an HTTP CONNECT proxy; the sender never forwards Sentry auth
  to a redirect or proxy challenge.
- Cap the complete serialized envelope at 64 KiB, response headers/body processing at
  64 KiB, and the entire call at five seconds. Provider response bodies are
  discarded and never logged or persisted.
- Honor Retry-After and relevant X-Sentry-Rate-Limits categories. A provider
  delay later than local equal-jitter backoff wins, bounded by the seven-day
  payload TTL. X-Sentry-Rate-Limits on an accepted 2xx still throttles later
  claims even though the current row is acknowledged.
- Treat 2xx as accepted; 408, 429, 5xx, timeout, and network failure as
  transient; and redirects or other 4xx as permanent. Policy and shutdown
  cancellation are supplied by the local gate, not inferred from HTTP.

The Electron visibility sender is a dedicated main-process, no-offline-cache,
no-auto-context transport. It does not reuse renderer CaptureException and does
not enable SDK breadcrumbs, device context, request context, modules, or
session persistence. Existing general desktop captures also route through main's
single consent generation and network gate; renderer-owned network Sentry is
removed. General captures may retain their separately approved event shape, but
their transport must expose acknowledged close and cache-purge behavior before
live opt-out can ship.

## Dispatcher

Start one daemon-lifetime dispatcher after store, policy, and observer setup and
before startup switch reconciliation.

For each cycle:

1. Re-read the authority and atomically expire payloads whose expires_at has
   elapsed; quarantine any pending row whose destination fingerprint no longer
   matches the current validated DSN.
2. Atomically claim one due, unexpired, matching-destination row only when
   policy generation and persisted provider throttles allow it.
3. Set a 30-second lease with the current consent generation and delivery epoch,
   without yet incrementing attempt_count.
4. Enter the delivery gate, re-read authority, and check expires_at again. If
   policy/generation/epoch changed, release as policy-cancelled. If expired,
   discard without a provider call.
5. Immediately before invocation, run a final attempt CAS requiring the lease,
   consent generation, destination fingerprint, and expires_at > now. Increment
   attempt_count and call the observer only when exactly one row changes.
6. Acknowledge only an accepted result and only with the matching lease token;
   persist accepted-response throttle headers in the same transaction.
7. Clear the lease and update available_at/provider throttle on a transient
   result.
8. Quarantine a typed permanent shape or authorization error.
9. Treat opt-out cancellation and shutdown cancellation as control outcomes,
   not transient or permanent provider failures. Opt-out purges the row;
   shutdown releases the lease and stops the dispatcher so it cannot hot-retry.

TTL eligibility linearizes at the successful final attempt CAS. A provider call
that began before expires_at may finish within its five-second bound; no call may
begin from a CAS evaluated at or after expiry. Opt-out cancellation remains
stronger and attempts to cancel/await even an already-started call.

Retry uses these base delays by attempt number:

~~~text
5 seconds
30 seconds
5 minutes
30 minutes
6 hours capped, with jitter
~~~

Each delay uses equal jitter in the inclusive range 50-100 percent of its base;
attempts after the fifth continue to use the six-hour base. available_at is set
from the retry decision time plus that delay.

An expired lease is reclaimable after a daemon crash. Every payload row is
removed at its occurred_at plus seven-day TTL; receipts follow the longer
incident-lifetime rule above.

Graceful shutdown stops new claims and lets the active call finish within its
five-second bound. If shutdown cancellation wins first, the dispatcher records
shutdown-cancelled, clears the lease, and exits without retrying. An abrupt crash
cannot clear the lease; that lease becomes reclaimable after 30 seconds by the
next daemon.

## Delivery Semantics

When reporting policy is enabled and the telemetry savepoint succeeds, local
state-to-outbox creation is exactly once per dedupe key for the semantic incident
lifetime because the payload row and longer-lived receipt are coupled to the
winning failure or marker CAS. An outbox-local invariant failure deliberately
trades that report for non-interference and is recorded only in the local log.

Remote delivery is at-least-once. If Sentry accepts an event and the daemon
crashes before delivered_at commits, the row is retried with the same EventID.
The stable EventID preserves event identity and the fingerprint groups duplicate
occurrences into one Sentry issue. Sentry fingerprints do not deduplicate sends;
duplicate remote occurrences remain possible and the design does not claim
remote exactly-once delivery.

The outbox cannot durably report total failure of the same SQLite database. If
SQLite is unavailable:

- The Manager retains the gate where safety requires it.
- No semantic agent-switch event uses direct Sentry fallback after a failed or
  ambiguous database attempt, because commit-applied/response-lost could cause
  both direct and outbox delivery.
- Privacy-safe local structured logging is the only switch-specific fallback.
- An existing process-level crash surface may still report an unrecovered daemon
  crash, but it is a separately grouped process incident, not a substitute
  switch report.

Outbox delivery errors do not recursively enqueue an outbox-delivery event.
While disconnected, the daemon exposes only local coarse backlog diagnostics.
attempt_count, delivery errors, and delay remain local dispatcher diagnostics.
They are not added to the remote envelope, so the EventID and serialized event
payload are identical across retries.

## Sentry Event Construction

Use a synthetic exception rather than the raw wrapped error:

~~~text
agent switch failure:
ownership_unresolved at chat_target_activation_commit
~~~

Title template:

~~~text
Agent switch failed:
chat / starting_target / chat_target_activation_commit
~~~

Safe tags are:

~~~text
feature=agent_switching
platform=daemon|renderer
report_kind
subsystem
mode
phase
failure_point
error_code
fault_code
execution
from_harness
target_harness
target_start_mode
runtime_backend
call_outcome
ownership
compensation
user_impact
release
classifier_callsite
~~~

Safe context is limited to:

~~~text
source_stop_confirmed
target_owner_committed
gate_retained
elapsed_time_bucket
~~~

## Capture-Site Stacks

Delayed delivery must not show a stack rooted in the outbox dispatcher.

For P0 and P1 internal faults, capture a bounded stack at semantic
classification time. Panics always capture their stack. User/environment P2
events do not require a stack.

Stored frames contain only:

- In-app package and function name.
- Repository-relative source filename.
- Line number.

They contain no absolute path, local username, locals, arguments, panic value,
or raw error text. The serialized stack is capped at 16 KiB.

## Privacy

Application-supplied remote event fields may contain only:

- Approved enums and booleans.
- Bounded duration and retry buckets.
- Sanitized in-app frames.
- Release, environment, channel, platform, and OS supplied by the adapter.

requested_at is never serialized. For a marker enrolled after later opt-in,
occurred_at is the opt-in enrollment time and elapsed_time_bucket is
not_applicable; no duration or timestamp is derived from the pre-consent request
or original marker classification.

They must never contain:

- Switch, session, project, workspace, or user IDs.
- Session titles, branches, issues, pull requests, or repository facts.
- Idempotency keys or request fingerprints.
- Models, configuration, argv, or environment values.
- Native conversation IDs, generation IDs, provider IDs, or runtime handles.
- Prompts, messages, handoff contents, transcript contents, hook payloads, or
  terminal output.
- Paths, artifact hashes, URLs, HTTP bodies, or raw error strings.

Serialization is deny-by-default. The adapter constructs a fresh event from the
allowlist and disables automatic SDK user, request, device, runtime, module,
environment, and breadcrumb contexts. A snapshot/allowlist test must fail when
a new field would leave the process without explicit approval.

The network provider can still observe transport metadata such as source IP.
Production enablement therefore requires verification of the Sentry
organization's IP-storage/scrubbing setting, data residency and retention, plus
an approved update to docs/telemetry.md explaining this failure stream and the
current-active-marker behavior on opt-in. The guarantee is no
application-supplied user/content identifier, not that the provider receives no
connection metadata.

The existing Sentry path scrubber remains a final defense, not the primary
privacy mechanism.

## Exact Capture Points

### HTTP and admission

- backend/internal/httpd/controllers/sessions.go validates and maps expected
  rejections. Expected 4xx outcomes do not report.
- backend/internal/session_manager/agent_switching.go admitAgentSwitch starts
  saga ownership only after CreateAgentSwitch is proven durable.
- Idempotent replay creates no event.
- Chat handoff arm and worker-start refusal flow through the normal durable
  failure path.

### Live TUI

executeAgentSwitch owns the safe local failure context. Its final defer already
knows source-stop certainty, target workspace preparation, target-runtime
ambiguity, target ownership, and compensation result. It selects terminal
failure versus a retained marker, then passes the typed fault into the atomic
store mutation.

### Live Chat

executeChatAgentSwitch owns the equivalent decision. Provider or controller
helpers return outcomes and never report independently. A failure after target
ownership commit cannot restore the source and must remain target-owned for
recovery.

### Terminal failure

failAgentSwitch is the semantic terminalization boundary. It reports only a
winning failed transition. Calling it on an already terminal row creates no
event.

### Delivery failure

FailAgentSwitchIfUnacknowledged enqueues only when its opposing predicate beats
the target acknowledgement. If acknowledgement or completion won, read-back
completes or returns the existing terminal row with no failure event.

### Recovery markers

The first successful same-state mutation in:

- markSourceStopUnconfirmed
- markSourceRestoreUnconfirmed
- markTargetStartUnconfirmed

creates a recovery_required row. An already-present marker creates nothing.

### Panics

The Chat inner recover must preserve debug.Stack before converting the panic.

The outer live-worker panic handler:

1. Retains the gate.
2. Performs one bounded reconciliation.
3. Persists panic as the cause of the resulting failed/marker mutation.
4. Enqueues one operational panic only if reconciliation creates no new durable
   classification.

The recovery-worker panic path follows the same rule.

A panic and its resulting terminal state are one incident, not a panic plus a
generic terminal failure.

### Reconciliation

ReconcileAgentSwitches and the per-mode reconciliation functions converge on
the same atomic terminal and marker paths. A per-switch reconciliation error
that produces no state change invokes the mandatory operational enqueue and
creates at most one recovery_attempt_failed event for the unchanged unresolved
incident.

ReconcileStartupSafety and daemon.Run may aggregate or abort startup but do not
recapture per-switch incidents.

### Maintenance and shutdown

Post-terminal artifact cleanup is a separate maintenance_failure. It never
changes completed or failed switch classification.

WaitAgentSwitchWorkers timeout produces one daemon-level aggregate incident.
It does not manufacture one failure for every active switch. Bare shutdown
context cancellation is not reportable.

## Duplicate Suppression

The following produce no new event:

- Matching idempotent request.
- Expected validation or active-switch conflict.
- Same recovery marker installed twice.
- failAgentSwitch called on a terminal row.
- Commit error followed by read-back proving success.
- Runtime error followed by exact proof of the intended side effect.
- Stale or duplicate lifecycle hook.
- Failure CAS that loses to acknowledgement.
- Startup wrapper around a reported reconciliation failure.
- Frontend reading a durable failed row.
- CLI polling timeout when the daemon already owns a recovery incident.
- Successful cleanup retry.

A recovery marker followed by a genuinely different terminal failure may create
a second event. It groups according to the semantic fingerprint.

## Recovery Semantics

### preparing_handoff

The source still owns the session. Restart closes the switch with
daemon_restart_pre_stop.

### stopping_source

- Exact fenced source generation alive: preserve source and retain/fail only
  through a state that keeps ownership safe.
- Exact fenced source generation dead: confirm the source-stop boundary, then
  compensate.
- Probe unknown: do not create a target; install or retain the nonterminal
  source_stop_unconfirmed marker and keep the gate.
- Session terminated: terminal source_session_terminated.

### source_stopped

Clean any recoverable target workspace preparation, restore the source once,
and terminalize with daemon_restart_post_stop. Failed cleanup or source restore
creates source_restore_unconfirmed and retains the gate.

### starting_target

For TUI:

- Missing handle: target_start_unconfirmed.
- Exact fenced target dead: clean and restore source.
- Exact fenced target alive with valid native identity: adopt only through the fenced
  activation transaction.
- Probe or cleanup unknown: retain the gate.

For Chat:

- Durable source ownership permits cleanup and source restoration.
- Unexpected owner/generation mismatch remains ownership_unresolved.

### target_ready

TUI does not replay an unconfirmed argv-bound continuation after restart.

Chat may reconstruct the exact target and relay once because no relay was
attempted before target_ready and the activation message ID is deterministic.

### delivering_context

- Durable acknowledgement: complete.
- No acknowledgement: never replay; fail delivery_unconfirmed.

### Terminal states

Reconciliation performs no switch side effect and creates no second event.

## Frontend Visibility

The renderer never reports the durable switch failure itself. The daemon owns
that incident.

The renderer may report only:

~~~text
visibility_transport
visibility_query
visibility_presentation
~~~

Electron main is the single visibility-report owner. Renderers send typed health,
expected-presentation, presentation-acknowledgement, cancellation, focus, and
online-state signals through preload IPC; they do not call Sentry directly for
agent-switch visibility. Main chooses the most recently focused live AO window
as the owner. Background windows cannot report, and changing focus cancels the
old owner's timer before the new owner can start one.

Main keeps one in-memory incident state machine per local dedupe key:

~~~text
healthy
→ suspect(first failure while focused and online)
→ reportable(exact grace elapsed without recovery or cancellation)
→ reported
→ healthy(after five continuous minutes of recovery)
~~~

One report is allowed per uninterrupted incident. A recurrence after five
healthy minutes is a new occurrence. Restarting Electron starts a new visibility
incident generation. Remote keys never contain switch IDs:

~~~text
transport | operation | active_or_history | release
query     | operation | active_or_history | release
presentation | presentation_kind | durable_state | release
~~~

For presentation only, main also keeps local switch_id plus switch updated_at in
the local key so multiple mounts cannot collide; those values are stripped
before event construction.

### Transport

Report only when SSE is disconnected, a full refresh attempted after the
disconnect also fails, the owner window stays focused and online, and the exact
grace period elapses:

- 15 seconds while a switch or recovery is active.
- 60 seconds for ordinary terminal history.

Successful SSE reconnect or full refresh returns the incident to healthy before
reporting. Blur, offline state, window destruction, app shutdown, navigation out
of the relevant surface, or consent disable cancels the timer and produces no
event. An outage healed by polling produces no event.

### Query

Start the same 15/60-second state machine on the first typed workspace or
switch-history query failure when transport itself is healthy. Any successful
query resets it. If SSE is disconnected and refresh fails, transport owns the
incident and query reporting is suppressed. The specific visibility report owns
the incident instead of generic API capture.

### Presentation

The route-level data coordinator sends expected_presentation with a random local
token when it has fetched a durable failed or recovery row that the current
surface is required to show. The actual failure/recovery component sends
presented from useLayoutEffect with the same token after its commit. Main allows
two seconds for the acknowledgement. A matching acknowledgement, route change,
unmount, owner-window blur, row supersession, or user dismissal cancels the
expectation. Expiry while the same focused route and durable row remain active
creates one visibility_presentation report.

Only the focused owner window participates. Main dedupes multiple mounts using
the local switch/revision key and accepts the first matching acknowledgement.
The token, switch ID, and updated_at never leave the device.

No visibility event is produced for:

- Successful rendering.
- Successful CDC or polling recovery.
- User dismissal.
- Navigation.
- A new mount discovering historical terminal state without showing a
  transient notification.

target_start_unconfirmed already has a recovery-visible presentation on current
main but is omitted from active polling. Adding it to the active polling set is
the Phase 0 correctness change; instrumenting proven failures of that existing
polling/presentation is Phase 4.

## Severity and Alerting

Every admitted semantic failure is eligible for Sentry when consent and
delivery permit it. Alerting, rather than capture omission, controls noise.

| Priority | Examples | Sentry level | Alert |
| --- | --- | --- | --- |
| P0 | Possible dual owner, unidentifiable live target, activation ownership unknown, controller published without durable ownership, gate without saga, cleanup that leaves a possibly live unidentifiable target | fatal | Immediate production page |
| P1 | No live owner, source restore unconfirmed, delivery unconfirmed, startup reconciliation blocked, post-stop panic, unrecoverable target/context | error | Notify on new issue; escalate on repetition |
| P2 | Missing binary, unauthorized target, safe pre-stop abort, safe rollback with source restored, proven-harmless post-terminal cleanup failure, proven frontend visibility failure | warning | Dashboard and routine triage |
| Suppressed | Expected validation/conflict, idempotent replay, stale callback, optional fallback, recovered transient interruption | none | No event |

The initial P1 escalation threshold is:

~~~text
5 events in 10 minutes
for the same fingerprint and release
~~~

Nonproduction channels create issues but do not page production responders.

Severity is derived from the complete outcome tuple, not error_code alone. For
example:

- target_start_unconfirmed with a possibly live unidentifiable target is P0.
- target_start_unconfirmed after target absence is proven but settlement
  remains blocked is P1.
- source_stop_unconfirmed is P1 because target creation has not legitimately
  begun but source availability is unknown.
- source_restore_unconfirmed is P1 because no live owner may remain.
- delivery_unconfirmed is P1 because target ownership exists but context
  execution is unknown.
- daemon_restart_pre_stop is P2 when source preservation is proven.

Consent breaches are never reported through the Sentry channel whose consent is
in question. They are detected by local invariant tests, release verification,
and off-channel privacy review.

## Runbooks

Each failure point must have a short runbook entry containing:

- Expected durable phase.
- Safe ownership interpretation.
- Whether source restoration is allowed.
- Relevant local logs and tests.
- User recovery action.
- Conditions that make the issue a release blocker.

## Correctness Prerequisites

Observability must not normalize or hide existing ownership defects. Production
alerts remain disabled until the following are addressed.

### Chat activation generation

Chat Service deliberately skips ClaimChatControllerGeneration when a pending
provider boundary exists, expecting ControllerReady to claim ownership
atomically. Agent switching always supplies such a boundary.

ActivateChatAgentSwitchTarget currently requires
controller_generation to already equal the target generation, while source stop
leaves the source generation in the session row. Activation must instead:

~~~text
WHERE controller_generation = expected_source_generation
SET controller_generation = target_generation
~~~

in the same transaction that commits target harness ownership, provider
conversation head, native identity, and target_ready.

A real Chat Service plus SQLite plus Session Manager integration test is
required. Fakes must not preclaim the target generation.

### ConPTY unknown versus dead

Registry write, parse, and read failures must not collapse to an empty list that
IsAlive interprets as definitive absence. The runtime contract must preserve:

~~~text
alive
dead
unknown
~~~

Otherwise restart can restore the source while an undiscoverable target remains
alive.

### Source-stop uncertainty and identity

Current recovery terminalizes source_stop_unconfirmed as failed. Before
reporting is enabled, a probe error or non-exact liveness result must instead
install the same-state source_stop_unconfirmed marker, retain the gate, and
remain recoverable. Go and SQLite transition validation must reject that code on
a failed row. Repeated startup and explicit recovery of the unchanged marker
must be idempotent.

Current Runtime.IsAlive checks a session/runtime handle and cannot always prove
the exact source process generation, especially for hook-native sources. The
switch recovery port adds a dedicated contract conceptually equivalent to:

~~~go
type FencedLiveness string

const (
    FencedAlive   FencedLiveness = "alive"
    FencedDead    FencedLiveness = "dead"
    FencedUnknown FencedLiveness = "unknown"
)

type FencedRuntimeRef struct {
    Backend        RuntimeBackend
    SessionHandle  RuntimeHandle
    Generation     ControllerGeneration
    NativeIdentity NativeSessionIdentity
}

type FencedRuntimeProber interface {
    ProbeFencedRuntime(context.Context, FencedRuntimeRef) FencedProbeResult
}
~~~

FencedProbeResult contains only Alive/Dead/Unknown plus a typed safe reason; raw
identities never enter telemetry. Recovery wording and ownership classification
use:

~~~text
exact source generation proven alive
exact source generation proven dead
unknown
~~~

A session-handle-only answer is unknown when a newer or stale generation could
exist. Recovery must retain the gate rather than treating it as exact proof.

Adapter requirements are:

- tmux proves alive/dead only for the exact mux target plus generation/native
  identity; missing identity or command ambiguity returns unknown.
- ConPTY requires a successfully parsed registry entry keyed by the session and
  generation plus its process-start identity. Registry read/parse/write failure
  is unknown; absence is dead only after a complete successful registry scan.
- Native Chat/controller recovery uses the fenced controller generation and
  pending provider boundary rather than an OS-process guess. A provider result
  without matching durable generation proof is unknown.
- Any current or future adapter that cannot implement an exact proof returns
  unknown. Runtime.IsAlive remains usable for non-ownership display checks but
  is forbidden in the switch-recovery ownership decision.

The supported-runtime matrix and fake adapters must exercise all three results;
production alerting stays off until every supported switch runtime either
provides exact proof or deliberately returns unknown.

### tmux cleanup ambiguity

If target creation partially succeeds and later cleanup fails, the adapter must
return enough structured evidence for the Manager to retain the gate. It must
not discard cleanup failure and return an apparently harmless empty handle.

### Telemetry consent consistency

Electron main, renderer, daemon, and pending outbox dispatch must use one
effective consent decision. Opt-out closes and drains the daemon delivery gate,
purges every daemon outbox payload, disables renderer/main capture, cancels SDK
transport queues, and deletes any Electron offline/cached envelopes before it
returns. If the Electron SDK cannot make those operations observable, it must
use a consent-gated transport owned by main rather than relying on SDK defaults.

While disabled, no telemetry receipt or payload is created. Core saga failure
state remains necessary product data. On later opt-in, the documented policy may
enroll a currently unresolved active marker as a new current-state incident,
using opt-in time and recovery_existing_marker rather than exporting its
pre-consent timestamp. Old terminal history is never enrolled.

### Panic and acknowledgement diagnostics

Chat panic stacks must be preserved. Lifecycle acknowledgement callers must use
the returned boolean so duplicate, stale, timeout-won, and impossible outcomes
can be classified.

### Recovery-state polling

target_start_unconfirmed must remain actively observed like source recovery
markers.

## Verification Strategy

### Taxonomy

Table tests require every failure point to map to:

- Subsystem.
- Report kind.
- Default severity.
- Allowed phases.
- Allowed error codes.
- Privacy-safe title.
- Runbook entry.

### SQLite atomicity

Test:

- Failed mutation and outbox commit together.
- Core transaction rollback leaves neither saga mutation nor payload.
- Injected outbox-only shape/constraint failure rolls back its savepoint, commits
  the saga failure_point/outcome, emits no direct report, and records one local
  invariant log.
- Database-wide commit failure follows the existing retained settlement path.
- Zero-row CAS creates no event.
- Repeated marker dedupes.
- Both acknowledgement/timeout orderings.
- Commit-applied/response-lost read-back.
- Consent disabled.
- Opt-out racing a failure mutation.
- Opt-out racing a claimed row before and during the bounded provider call.
- No provider call begins after delivery-gate closure.
- Crash during opt-out followed by fail-closed startup synchronization.
- Policy file atomicity, 0600 permissions, first packaged default, environment
  hard vetoes, and direct-headless default-off behavior.
- Power loss before file flush, after file flush, after atomic replace, and
  before/after parent-directory flush; no unacknowledged off generation is
  reported complete and no uncertain file rematerializes enabled.
- Main/daemon/renderer stale-generation rejection and matching-generation
  acknowledgements.
- Daemon apply-policy re-reads the 0600 authority file; a forged loopback body
  cannot enable an off or mismatched generation.
- consent_generation has one desktop writer while delivery_epoch and headless
  boot tokens never mutate it.
- Every row of the headless file/environment truth table.
- Disable with daemon unavailable, renderer destroyed, and a later daemon or
  renderer startup.
- Daemon misses prepare-disable, then observes the off file through synchronous
  pre-send revalidation and the one-second watcher; opt-out stays pending until
  active calls are accounted for.
- Every outbox payload status purged while payload-free receipts remain.
- Re-enable without terminal-history upload.
- Re-enable enrolls only an unresolved marker with no prior receipt and uses
  opt-in time.
- Session deletion with pending, delivered, and discarded payloads.
- Switch receipt deletion with core data while the consented payload survives
  only to its hard TTL.
- Terminal/run receipt seven-day expiry, unresolved receipt retention, and
  retain_until assignment when the exact unresolved fingerprint changes.
- Standalone enqueue versus switch deletion in both commit orders; deletion-first
  creates no receipt or payload.
- ALTER migration preserves the latest CDC triggers and indexes and produces no
  manual change_log write.

### Runtime and ownership

Test:

- Source Destroy error with exact dead proof.
- Source probe unknown.
- Session-handle liveness without exact source-generation proof.
- source_stop_unconfirmed remains nonterminal and idempotent across startup and
  explicit recovery.
- Target Create returning handle plus error.
- Target Create returning empty handle after possible effect.
- Exact-generation mismatch.
- Target probe plus cleanup uncertainty.
- Activation commit with response loss.
- Source-owned read-back.
- Target-owned read-back.
- Neither-owner read-back.
- ConPTY registry failure.
- tmux partial-create cleanup failure.

### Chat

Test:

- Real ControllerReady activation with SQLite.
- Provider failure before commit.
- Resume identity mismatch.
- Activation commit followed by callback error.
- Controller return without durable ownership.
- Stable-ID relay after response loss.
- Restart at target_ready.
- Restart at delivering_context.
- Durable acknowledgement followed by restart.
- Panic before and after ownership commit.

### Lifecycle acknowledgement

Run both race orders:

~~~text
acknowledgement then timeout CAS
timeout CAS then late acknowledgement
~~~

Exactly one durable terminal outcome wins. Exactly one failure event exists only
when timeout wins. Duplicate, stale, and wrong-generation hooks create none.

### Restart

Run reconciliation twice from:

- preparing_handoff.
- stopping_source with source alive, dead, and unknown.
- source_stopped.
- starting_target with no handle, dead exact target, live exact target, and
  inconclusive target.
- TUI target_ready.
- Chat target_ready.
- delivering_context with and without acknowledgement.
- completed and failed.

The second reconciliation must be idempotent and produce no repeated external
side effect or outbox row.

### Panic and shutdown

Test panic:

- Before source stop.
- After source stop.
- After external target creation.
- After target ownership commit.
- Inside recovery.

Test normal shutdown cancellation and worker-wait timeout. The latter creates
one daemon aggregate incident, not one per switch.

### Dispatcher

Use a fake observer or httptest only. Test:

- Startup drain.
- Lease claim and expiry.
- Crash after provider acceptance before local acknowledgement.
- Stable EventID across retries.
- Byte-identical event payload across retries.
- Retry after app/release/OS metadata changes still sends the stored canonical
  event and deterministic envelope bytes.
- Old envelope_encoding_version golden fixtures remain byte-identical after an
  upgrade; row/event/header EventID mismatch and final envelope over 64 KiB
  quarantine without a provider call.
- Backoff and jitter bounds.
- Timeout, 429, and 5xx retry.
- HTTP 2xx acknowledgement and nonretryable 4xx quarantine.
- DSN parsing, production HTTPS enforcement, auth construction, redirect
  refusal, standard proxy/TLS behavior, and 64-KiB request/response bounds.
- Retry-After and X-Sentry-Rate-Limits override shorter local backoff without
  exceeding payload TTL.
- Accepted 2xx throttles later claims, category/all throttles survive restart,
  and destination change resets only the new destination's throttle state.
- Claim just before expiry, expiry before gate entry, expiry before final attempt
  CAS, and expiry during an allowed bounded call.
- Default/override DSN rotation quarantines mismatched destination fingerprints
  and never sends an old row to the new project.
- Response-lost-after-acceptance retries the same EventID and may produce a
  duplicate Sentry occurrence grouped into the same issue.
- No-op observer cannot acknowledge or drain an existing row.
- Permanent malformed payload quarantine.
- Pending row never delivered after seven days without connectivity.
- Seven-day payload expiry with longer unresolved-incident receipt retention.
- Opt-out purge and policy/shutdown cancellation outcomes.
- Re-enable behavior.
- Ambiguous SQLite commit never invokes a direct semantic Sentry fallback.
- No mutation of switch ownership on delivery failure.
- No recursive reporting.

### Privacy

Serialize every report kind and reject payloads containing:

- Absolute paths.
- Workspace paths.
- UUID-like native or generation values.
- Prompt, terminal, hook, or handoff content.
- Environment or command arguments.
- Artifact hashes.
- Idempotency keys.
- URLs, branches, issues, or pull-request facts.
- Raw error strings.

Stack tests verify path removal, allowed frame fields, size bounds, and absence
of panic values.

### Reporting ownership

Test pre-admission HTTP ownership and post-admission saga ownership through an
unwrapped error, multiple %w layers, controller translation, request-local
capture metadata, the generated API envelope, and renderer deserialization. A
post-admission synchronous 5xx produces the durable saga event only; HTTP and
renderer capture counters remain zero. A true pre-admission 5xx remains
HTTP-owned.

### Frontend visibility

Test:

- Missed CDC healed by refresh.
- SSE outage healed inside grace.
- SSE and refresh unavailable beyond grace.
- Durable failed row rendered normally.
- Fetched row missing its presentation.
- Multiple mounts/windows.
- Focus transfer between windows and background-window suppression.
- ao.agent_switch.visibility_failure kill-switch suppression in Electron main.
- Exact 15-second active and 60-second history timers.
- Five healthy minutes before a visibility recurrence can report again.
- Presentation token acknowledged from useLayoutEffect, cancelled by navigation,
  and expired after two seconds only on the still-current route/revision.
- target_start_unconfirmed polling.
- Suppression of duplicate daemon outcome reporting.

## Combination Coverage

Use pairwise coverage across:

~~~text
mode       tui | chat
direction  claude-to-codex | codex-to-claude
start      fresh | resume
execution  live | startup_reconcile | explicit_recovery
~~~

Every boundary-specific call outcome is exercised at least once:

~~~text
no_effect_failure
committed_response_lost
effect_unknown
stale
timed_out
cancelled
panic
cleanup_failed
~~~

Ownership-critical cases run in every applicable mode even when that exceeds
pairwise coverage.

## Rollout

### Phase 0: correctness

- Fix Chat activation generation.
- Fix ConPTY unknown/dead semantics.
- Make source_stop_unconfirmed a retained nonterminal marker and add exact
  fenced-source identity semantics.
- Preserve tmux cleanup ambiguity.
- Unify telemetry consent through the durable generation protocol and make
  Electron main the sole desktop Sentry network sender.
- Fix target-recovery polling and panic-stack loss.

### Phase 1: durable foundation

- Add failure taxonomy.
- Add failure_point.
- Add policy and outbox migration.
- Add new atomic and standalone store operations and tests without yet replacing
  existing Manager calls. The new methods validate typed faults when invoked;
  dormant old methods remain only as a compatibility seam while remote delivery
  is feature-gated off.
- Keep remote delivery disabled.

### Phase 2: delivery pipeline

- Add provider-neutral observer.
- Add the dedicated synchronous, status-aware Sentry envelope sender; do not
  treat global SDK enqueue/Flush as per-event acknowledgement.
- Add dispatcher, retry, purge, and privacy verification.
- Exercise only fake and nonproduction sinks.

### Phase 3: Manager instrumentation

- Instrument admission-after-commit, TUI, Chat, compensation, recovery, panic,
  cleanup, and shutdown.
- Add all duplicate-suppression rules.
- Prove successful switches create no rows or events.
- Migrate every reportable state mutation to the typed store methods, then make
  legacy methods reject reportable transitions before enabling the feature.
- Add reporting_owner to the API error DTO, regenerate OpenAPI and frontend
  schema types together, and verify wrapped-error suppression in HTTP and the
  renderer.

### Phase 4: frontend visibility

- Instrument the already-fixed recovery polling path.
- Add visibility-only classifications.
- Suppress renderer and HTTP duplicates.

### Phase 5: production alerts

- Enable the failure stream.
- Enable P0, P1, and P2 issue creation.
- Turn on P0 paging after nonproduction validation.
- Publish runbooks.
- Approve the telemetry/privacy documentation and verify Sentry organization IP
  handling, retention, residency, and disabled automatic contexts.
- Review grouping after the first release before changing thresholds.

These phases should ship as separate focused pull requests rather than one
large pull request.

## Release Gates

The feature is complete only when:

1. A successful switch creates zero outbox and Sentry events.
2. With reporting policy enabled, every valid winning failed or first-marker CAS
   creates exactly one receipt and local payload. An injected telemetry-local
   invariant failure commits the core outcome without either; disabled policy
   also creates neither while still committing core saga diagnostics.
3. An acknowledgement-winning delivery race creates no failure.
4. Restart drains pending failures with the same EventID.
5. No application-supplied raw content or user/session/project identifier
   crosses the Sentry boundary; automatic SDK contexts are disabled and
   provider connection-metadata policy is verified and documented.
6. Telemetry-local schema/constraint failures, Sentry failures, and network
   failures cannot change runtime ownership, compensation, or terminal
   settlement; database-wide commit failure remains a core storage failure.
7. Daemon, HTTP, renderer, and startup wrappers do not duplicate incidents.
8. TUI and Chat pass the ownership and restart matrix.
9. Chat activation and ConPTY false-death prerequisites are fixed.
10. Opt-out is linearized through the shared delivery gate, permits no new
    provider call after completion, purges every payload status and every
    Electron cached envelope, and leaves only payload-free dedupe receipts.
11. The status-aware Sentry sender acknowledges only a 2xx provider response and
    classifies retry, quarantine, policy cancellation, and shutdown cancellation
    distinctly.
12. source_stop_unconfirmed is nonterminal, exact source/target identity is not
    inferred from an ambiguous handle probe, and all current-main correctness
    prerequisites pass.
13. docs/telemetry.md and Sentry organization privacy settings are approved
    before production enablement.
14. The durable policy generation, loopback acknowledgement protocol, sole-main
    desktop sender, and direct-headless default-off paths pass cross-process
    restart and unavailability tests.
15. Delayed retries send byte-identical canonical envelopes across app upgrades,
    and standalone operational enqueue cannot create data after its switch is
    deleted or superseded.
16. Final-attempt CAS enforces consent, destination, lease, throttle, and TTL;
    rows never cross Sentry projects after DSN rotation, and only calls begun
    before expiry may finish after it.

## Physical and Privacy Limits

No client-side failure pipeline can deliver an event when:

- The user has disabled telemetry.
- No Sentry provider is configured.
- The device never reconnects.
- The application is removed before pending delivery.
- SQLite and the network both fail before any durable local fact can be
  recorded.

The design intentionally does not add successful heartbeats to detect those
conditions because that would violate the failure-only requirement.
