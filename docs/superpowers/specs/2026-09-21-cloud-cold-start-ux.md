# Spec: cloud cold-start interaction and execution readiness

Status: implementation in progress, reviewed 2026-09-22

Builds on:

- `docs/superpowers/specs/2026-09-17-cloud-browser.md`
- `docs/superpowers/plans/2026-09-17-cloud-browser-stage1-vm-automation.md`
- `docs/cloud-browser-stage1.md`
- `cloud/internal/reconcile/reconciler.go`
- `cloud/cmd/ao-worker/main.go`
- `cloud/internal/workertransport/supervisor.go`
- `cloud/internal/workertransport/stream.go`
- `cloud/internal/worker/checkout.go`
- `frontend/src/renderer/components/TaskComposer.tsx`
- `frontend/src/renderer/components/TerminalPane.tsx`
- `frontend/src/renderer/lib/cloud-orchestrator.ts`

This document specifies the complete cold-start experience. It covers the
control plane, worker, repository preparation, harness launch, terminal input,
browser preparation, renderer behavior, timing telemetry, tests, and rollout.

---

## 1. Goal and non-goals

### 1.1 Goal

Give the user an interactive Cloud session surface immediately while retaining
fresh compute for every session. The user can type and submit complete messages
before the remote workspace is ready. AO stores those messages durably, starts
the coding harness only after the repository and restored state are complete,
then delivers the messages in order.

The intended experience is:

```text
User presses Cloud
  -> pending session surface appears
  -> composer accepts input
  -> session and first prompt become durable
  -> fresh compute starts
  -> worker connects
  -> checkout and independent startup work run concurrently
  -> repository and restored state become ready
  -> coding harness starts in the complete workspace
  -> first terminal frame renders and raw terminal input unlocks
  -> Chromium starts only if explicit browser intent exists
```

The primary product target is a pending session surface in 100 ms at p95, then
a usable remote harness in 15 seconds at p50 and 45 seconds at p95. Those
numbers remain targets until hosted measurements establish them.

### 1.2 Non-goals

- Warm pools, pre-created sandboxes, or standby workers.
- Letting the coding harness perform the initial repository clone.
- Starting the harness against an empty or partially restored workspace.
- Treating raw terminal keystrokes as durable messages.
- Predicting browser intent from prompt keywords or URL-like text.
- Starting Chromium for sessions without explicit browser intent.
- Making shallow clone the default and breaking history-dependent work.
- Including external service response time in infrastructure readiness claims.
- Claiming hosted percentiles from a local Docker benchmark.

## 2. Decisions

### D1. Cold starts remain the only allocation model

Every session receives fresh compute after the user requests it. AO may use
ordinary provider image caches and regional registries, but it does not reserve
running user compute in advance.

### D2. Interaction readiness and execution readiness are separate

The renderer becomes interaction-ready before a sandbox exists. It accepts
complete messages through the composer and gives each submission a visible
state. Execution readiness occurs later, after checkout, restore, harness
launch, and terminal attachment.

Raw terminal input remains disabled until execution readiness. Escape
sequences, control keys, resize operations, and partial input are meaningful
only when a real PTY exists.

### D3. AO owns checkout and restore

The worker prepares the repository. The harness never performs the initial
clone. This gives AO deterministic progress, bounded credentials, consistent
failure reporting, restore ordering, and comparable latency measurements.

### D4. The harness is present in the worker image

Normal startup never installs the harness. The worker image contains the
supported harness binaries and verifies availability before launch. Image
construction, registry placement, and provider image caching happen outside a
session's critical path.

### D5. Independent worker preparation runs concurrently

After bootstrap succeeds, the worker starts the workspace transport and runs
independent work in parallel:

1. Obtain the checkout grant, clone or initialize the repository, configure
   Git, and restore durable session state.
2. Fetch the harness credential.
3. Reserve the agent terminal identity and its early-input buffer.
4. Bind browserd and, when early browser intent exists, prepare Chromium.
5. Publish worker metadata, heartbeat, and startup milestones.

The harness process starts only after the required workspace and credential
results have joined successfully.

### D6. The initial prompt has one delivery source

Direct create requests store the initial prompt in `session.prompt`, and the
worker passes it through the launch context. Click-triggered preparations have
an empty launch prompt. Their commit request records the first prompt as one
durable turn instead. A session uses exactly one of these delivery paths.

Messages submitted after creation are durable turns. They are delivered in
sequence after the agent terminal becomes ready. Retries use idempotency keys
so an ambiguous client response cannot duplicate a message.

### D7. All terminal input crosses one admission gate

Polled terminal input and persistent-stream input use the same supervisor
method. Before the agent PTY is ready, complete message data may be buffered or
left in the durable queue, and only the latest pending resize is retained.
Nothing writes directly to the agent PTY around this gate.

### D8. Startup is event-driven with bounded fallback polling

Session creation, resume, restore, and retry signal the reconciler immediately.
The existing periodic reconciliation remains a recovery mechanism. When
workspace readiness changes, the local worker claim loop is also woken
immediately so it does not wait for the long-poll timeout.

### D9. Browser intent is explicit

Browser intent is recorded by a viewer attachment, explicit task metadata, or
the first browser verb. Early intent may prepare Chromium in parallel with
checkout after the worker connects. Late intent starts Chromium on demand. No
prompt classification is used.

### D10. The known default branch constrains initial transfer

The production clone is non-shallow and checks out the project's known default
branch with `--single-branch --filter=blob:none --no-tags`. Commit and tree
history for that branch remain available. Historical blob contents arrive on
demand, while checkout materializes the current worktree before the harness can
run. Origin validation, task-branch creation, credential renewal, and push stay
unchanged.

### D11. New Task starts a hidden cold preparation

Opening a Cloud task composer creates a fresh promptless worker session. The
session is hidden from normal lists, begins provider allocation immediately,
and expires after two minutes unless Start Task commits it. Submit changes the
display name, reveals the session, and queues the first prompt atomically.
Closing the composer detaches it and leaves a two-minute reconnect grace.
Reopening with the same compute-affecting settings reuses that preparation.
Changing harness or sandbox provider cancels it and starts a replacement.
Lease renewal, reconnect, idle expiry, and late-submit recovery are specified
in `2026-09-23-cloud-preparation-reconnect-grace.md`.

### Baseline implementation audit

This is the baseline the implementation plan changes. It separates code that
already exists from behavior that still needs integration or correction.

| Area | Current baseline | Required change |
| --- | --- | --- |
| Compute allocation | Fresh sandbox per session | Keep cold-only allocation |
| Worker image | Harness, Chromium, and browser helper are baked in | Keep installation outside session startup |
| Browser process | browserd is loopback-only and Chromium starts lazily | Add only explicit early-intent scheduling |
| Initial prompt | Stored on the session and returned in launch context | Use launch context for direct create or one durable turn for preparation commit |
| Follow-up messages | Stored as durable turns with worker notification | Accept during startup, guarantee ordering and idempotency |
| Reconciliation | Durable reconciliation runs on a two-second interval | Add immediate wake signals and retain interval fallback |
| Checkout result | One channel closes after either success or failure | Return a typed result and block launch on failure |
| Credential fetch | Starts after checkout and restore | Run beside workspace preparation |
| Terminal allocation | Starts after checkout and restore | Reserve identity and buffers beside workspace preparation |
| Supervisor gate | Reservation and buffering methods exist | Wire them through the worker main path |
| Stream input | Persistent stream writes directly to the PTY | Route it through the supervisor gate |
| Worker work wait | Server long-poll can wait five seconds | Wake locally when readiness changes |
| Renderer startup | Session and terminal views exist | Keep composer usable on a pending session surface |
| Progress | Some lifecycle and timing projection exists | Cover every stable product state and failure phase |
| Measurement | Deterministic local smoke measurement exists | Add representative, failure, desktop, and hosted runs |

## 3. Readiness contract

### 3.1 Interaction ready

Interaction readiness is a renderer contract. It is reached when:

- the pending session route is visible;
- the composer accepts text;
- the startup attempt has a stable client identifier;
- a submitted message is either held locally with a visible `Saving` state or
  acknowledged as durable by the server;
- navigation away does not silently lose an acknowledged message.

The pending route may appear before the create request returns. Once the server
returns the durable session identifier, the renderer binds the pending route
to that session without replacing the surface.

### 3.2 Workspace ready

Workspace readiness is a worker contract. It requires:

- the repository clone or scratch initialization completed;
- repository Git configuration completed;
- preserved uncommitted work restored when applicable;
- the conversation transcript restored when applicable;
- repository-level instruction files are available;
- checkout failure has not occurred.

Workspace readiness does not by itself permit raw PTY input.

### 3.3 Execution ready

Execution readiness requires:

- workspace readiness;
- a valid harness credential;
- the harness command built against the completed workspace;
- the agent terminal identity allocated;
- the harness process and PTY started;
- the initial launch prompt handed to the harness exactly once;
- the terminal transport able to accept gated input.

The worker then publishes `agent.ready`. The renderer's stronger user-facing
ready point is `terminal.first_frame`, after the first completed terminal paint
and input enablement.

### 3.4 Browser ready

Browser readiness requires explicit intent plus successful Chromium endpoint
discovery and either the first successful command or the first viewer
keyframe. Browser readiness is independent of agent execution readiness.

## 4. End-to-end flow

### 4.1 Create and immediate interaction

1. The user presses New Task for a Cloud project.
2. The renderer opens the editable composer and immediately posts a hidden
   preparation with a stable idempotency key.
3. The server atomically stores a promptless session, its two-minute expiry,
   and sandbox intent. Fresh compute starts while the user types.
4. If the user submits before the preparation response, the renderer stores the
   complete message in memory under the attempt identifier and displays
   `Saving session`.
5. Start Task commits the prepared session and the first durable turn in one
   database transaction.
6. The renderer binds the pending route to the durable identifier and refreshes
   the normal session list.
7. Later messages are posted in submission order with stable
   idempotency keys.
8. The session surface advances through observed startup states without being
   replaced by an empty terminal.

If preparation or commit definitively fails, locally held text remains visible
and editable. Ambiguous retries reuse the same idempotency key. Closing an
unsubmitted composer detaches it for a two-minute reconnect grace. The server
expiry covers idle, crashed, and disconnected clients.

### 4.2 Reconciliation and compute

1. Session persistence emits a reconcile wake signal.
2. A reconciler claims the sandbox intent immediately.
3. The provider creates fresh compute.
4. Provider state changes publish milestones and wake reconciliation.
5. When the sandbox reaches running, AO starts or contacts the worker without
   waiting for the next fixed ticker.
6. The authenticated worker bootstrap establishes the current worker epoch.

The periodic ticker remains active to recover from lost signals and transient
failures. Signals reduce normal latency but are never the only source of
correctness.

### 4.3 Worker startup graph

```text
worker bootstrap accepted
  |
  +-> start workspace transport
  |
  +-> workspace lane
  |     checkout grant
  |       -> clone or scratch init
  |       -> Git configuration
  |       -> restore files and transcript
  |       -> workspace-ready result
  |
  +-> credential lane
  |     harness credential result
  |
  +-> terminal lane
  |     reserve terminal identity and input buffer
  |
  +-> browser lane
  |     bind browserd
  |       -> optional early Chromium prepare
  |
  +-> metadata lane
        worker.ready, heartbeat, capabilities

workspace-ready + credential + terminal reservation
  -> build harness command
  -> start harness PTY
  -> hand off initial prompt once
  -> open terminal input gate
  -> wake work claim loop
  -> publish agent.ready
  -> first renderer paint
  -> terminal.first_frame
```

Each required lane returns a typed result. Cancellation propagates through the
shared worker context. A failed required lane cancels dependent work and cannot
be mistaken for success.

### 4.4 Follow-up message delivery

1. The API validates and stores the complete user message as a durable turn.
2. The store notifies the waiting worker.
3. Before execution readiness, the turn remains unclaimed or safely buffered.
4. Opening the terminal input gate wakes the claim loop immediately.
5. The worker claims turns in durable sequence order.
6. The supervisor writes each turn through the single input admission path.
7. Completion or failure uses the existing turn result contract.

The direct-create prompt is not present in this turn sequence. A preparation
commit stores its first prompt as the first durable turn instead. Each session
uses exactly one path, preventing the prompt from arriving through both launch
context and the queue.

### 4.5 Browser flow

For early intent:

1. The control plane persists browser intent before the worker exists.
2. Bootstrap returns the intent for the current worker epoch.
3. browserd binds its loopback service.
4. Chromium preparation runs beside checkout where resource limits allow.
5. Browser failure is reported without failing the agent startup lanes.

For late intent:

1. A viewer attachment or browser verb records intent.
2. The control plane wakes the connected worker.
3. browserd lazily starts Chromium.
4. All concurrent callers join the same launch.
5. The first keyframe or command completion records browser readiness.

## 5. Required implementation changes

### Click-triggered session preparation

Add dedicated prepare and commit endpoints. Preparation creates the ordinary
durable session and sandbox rows but marks the session hidden with an expiry.
Commit clears that marker, updates the display name, and appends the first
prompt as one durable turn. Normal list queries exclude preparations. The
reconcile pass converts expired preparations to deletion intent before claiming
due sandboxes.

The renderer starts preparation when the Cloud composer mounts. It retains one
attempt across pending-route binding, cancels on close or selection changes,
and falls back to the direct create path only when no preparation exists.

Required tests:

- click starts one preparation before submit;
- submit commits the prepared identifier exactly once;
- close detaches for a two-minute reconnect grace;
- compatible reopen reuses the same preparation;
- selection changes request deletion and replacement;
- expiry requests deletion without a connected client;
- normal session lists hide preparations until commit;
- credential and orchestrator preflights overlap;
- prepare and commit retries reuse stable idempotency keys.

### 5.1 Reconciler wake path

Current behavior includes a fixed two-second reconcile ticker. Add a
process-local wake channel or equivalent coalescing signal. Signal it after:

- session creation commits;
- resume commits;
- restore commits;
- retry becomes eligible;
- relevant provider or worker state changes.

The reconciler drains duplicate signals and performs the normal durable query.
The database remains the source of truth. Keep the ticker as a fallback and
keep provider retry limits unchanged.

Required tests:

- create wakes a sleeping reconciler;
- a burst of signals coalesces without blocking writers;
- lost signals are recovered by the ticker;
- cancellation stops the wake loop;
- two reconcilers do not double-claim an intent.

### 5.2 Typed workspace preparation result

Replace the bare completion channel used by checkout and rehydration with a
typed result carrying success or the bounded failure. Closing a channel must no
longer mean both success and failure.

On failure:

- never call the harness builder;
- never start the harness process;
- discard any reserved terminal command and cleanup resources;
- publish the precise failed milestone;
- keep submitted messages durable for retry;
- show a repository preparation failure in the session surface.

On success, restore must finish before the harness command is built. This is
required because resume detection and repository instructions are read during
command construction.

### 5.3 Parallel credential and terminal preparation

Move credential fetch and agent terminal reservation out from behind checkout.
They begin after worker bootstrap and complete independently.

Terminal preparation at this stage means reserving identity, sizing state, and
buffering capability. It does not mean starting the harness in an incomplete
working directory. Any cleanup function returned while preparing the harness
must be owned by one result and invoked exactly once on cancellation or error.

Required tests:

- checkout and credential fetch overlap;
- a credential failure cancels launch but does not corrupt checkout state;
- a checkout failure cleans up a prepared command and terminal reservation;
- cancellation during either lane exits without a leaked process;
- restored sessions build only after transcript restoration.

### 5.4 Supervisor input admission

Use one method for terminal input from both transport sources:

- durable `terminal.input` requests;
- persistent terminal-stream input frames.

Before execution readiness:

- complete submitted message bytes are preserved in durable turns;
- any accepted terminal bytes are bounded by an explicit memory limit;
- resize requests collapse to the latest size;
- no bytes reach the PTY;
- acknowledgement does not imply execution.

After the gate opens, flush buffered data in accepted order, then admit live
input. Stream redial and fallback polling must preserve the same ordering and
at-most-once behavior.

The existing direct stream write to `terminal.pty` must be routed through this
admission method. Supervisor reservation and buffering APIs must be wired from
the worker main path, not left as test-only behavior.

Required tests:

- polled input is held before readiness;
- streamed input is held before readiness;
- both paths flush in deterministic order;
- the latest resize wins;
- a stream reconnect does not duplicate buffered input;
- memory limits reject or defer excess input safely;
- checkout failure discards non-durable raw bytes and preserves durable turns.

### 5.5 Immediate worker claim wakeup

The control plane already wakes `WaitForWork` when new work is stored. Add a
local wake source for readiness transitions. Otherwise a notification that
arrived while the workspace gate was closed can be consumed, leaving the
worker in a long-poll for up to five seconds after readiness.

`MarkWorkspaceReady`, or the final execution-ready transition if the gate is
renamed, must interrupt the local wait immediately. Claim requests remain the
source of truth.

Required test: enqueue a turn before readiness, place the worker in its wait
path, open readiness, and assert a claim begins within 250 ms without a new
server notification.

### 5.6 Pending session and composer

The renderer owns a startup attempt before it owns a durable session.

Add or complete:

- a pending route keyed by the startup attempt identifier;
- a stable composer that stays mounted while identifiers bind;
- ordered local holding for sends made before create acknowledgement;
- per-message `Saving`, `Queued`, `Sending`, and `Failed` states;
- create and message idempotency keys;
- navigation recovery for acknowledged messages;
- input lock only for raw terminal mode, not the message composer;
- focus retention across startup phase changes.

The UI must never claim that a locally held message is durable. `Saving` means
the client is still trying to establish that fact.

### 5.7 Startup progress projection

Expose one stable set of product states derived from milestones:

| Product state | Entry | Exit |
| --- | --- | --- |
| Saving session | Cloud action | create acknowledgement |
| Allocating workspace | session durable | provider create begins |
| Starting workspace | provider create begins | worker connects |
| Preparing repository | worker connects | workspace result succeeds |
| Starting agent | workspace succeeds | first terminal frame |
| Ready | first terminal frame | session stops or fails |
| Starting browser | explicit browser intent | browser ready or browser failure |

The renderer keeps a single surface and changes status text. It does not flash
an empty terminal between states. Browser progress is independent and may be
shown beside the agent state.

Time-based copy supplements observed states:

- after 20 seconds, show the current phase and `Still working`;
- after 45 seconds, show `Taking longer than usual` with the current phase;
- after 90 seconds, offer a retry that preserves durable input;
- on terminal failure, show the owning phase and a bounded error.

Do not show a countdown. Do not show provider-specific estimates until the
sample threshold in section 9 is satisfied.

### 5.8 Checkout strategy experiment

Keep the production default as the current non-shallow clone with `--no-tags`.
Benchmark these candidates without changing default behavior:

1. Current checkout.
2. Partial clone with `--filter=blob:none`.
3. Partial clone plus provider-specific protocol tuning when supported.

Test small, medium, and large repositories. Include repositories with long
history, submodules, large-file pointers, generated assets, and monorepo
layouts. After checkout, exercise status, diff, blame, log, branch creation,
rebase, fetch, and the first access to an omitted blob.

Do not use `--depth=1` as the default. It removes history that common coding
workflows require and moves failure into the user's first operation.

### 5.9 Browser scheduling

The worker image already carries Chromium and browserd. The process remains
lazy. If explicit intent exists at bootstrap, prepare it alongside network-bound
checkout work. On constrained single-CPU shapes, avoid competing with local Git
indexing. Hosted measurements determine the scheduling policy per shape.

Browser launch remains optional. Browser preparation failure never changes
agent readiness, and agent startup failure does not discard a successful
browser diagnostic path.

## 6. Data and API contracts

### 6.1 Startup attempt identity

Every create attempt has one idempotency key generated by the renderer. The
server returns the same committed session for a replay of that key and payload.
An incompatible payload with the same key fails explicitly.

Every follow-up message has its own idempotency key and client sequence. The
server de-duplicates by user, session, and key, then assigns the durable server
sequence.

### 6.2 Milestone event shape

Each startup milestone carries:

```text
attempt_id
session_id
worker_epoch, when known
milestone
observed_at
elapsed_ms, when calculated by the observing boundary
provider
region
shape
worker_image_digest
repository_size_bucket
restored
image_cache_state, when the provider exposes it
result
bounded_error_code, on failure
```

The exact DTO names are selected during implementation. Any controller DTO
change follows the repository's code-first API flow, including regenerated
OpenAPI and frontend types.

### 6.3 Required milestones

```text
session.requested
session.accepted
sandbox.provisioning
sandbox.running
worker.connected
worker.ready
checkout.started
checkout.completed
restore.started
restore.completed
workspace.ready
agent.launch_started
agent.ready
terminal.first_frame
browser.intent
browser.launch_started
browser.endpoint_ready
browser.first_keyframe
```

Failure events use the same attempt and owning phase. They do not manufacture a
later success milestone.

### 6.4 Privacy

Timing events never contain:

- prompt or message text;
- repository contents or credential material;
- terminal input or output;
- visited URLs;
- browser frames;
- local filesystem paths containing user data.

Repository size is bucketed. Attempt identifiers used in renderer analytics are
hashed or replaced with an opaque telemetry identifier.

## 7. Timing targets

These are product targets, not established hosted percentiles.

| Measure | Start | End | p50 | p95 |
| --- | --- | --- | ---: | ---: |
| Pending surface | Cloud action | composer visible and editable | 50 ms | 100 ms |
| Session accepted | request sent | durable create response | 250 ms | 500 ms |
| Durable follow-up | send action | durable message acknowledgement | 250 ms | 500 ms |
| Provision pickup | create commit | provider create begins | 100 ms | 250 ms |
| Compute | create acknowledgement | worker connected | 5 s | 20 s |
| Checkout start | worker connected | checkout request begins | 100 ms | 250 ms |
| Ready queue release | execution gate opens | first pending claim begins | 50 ms | 250 ms |
| User ready | Cloud action | first terminal frame with input enabled | 15 s | 45 s |
| First useful output | Cloud action | first non-startup harness output | 20 s | 60 s |
| Late browser | explicit browser intent | command success or first keyframe | 2 s | 3 s |
| Early browser increment | first terminal frame | browser ready | 0 s | 0 s |

The existing 180-second worker startup window remains a repair threshold. The
Chromium 15-second launch timeout remains a failure boundary. Neither value is
an expected user wait time.

## 8. Current evidence and limitations

### 8.1 Five-run local baseline

The local Docker measurement used the deterministic smoke harness and the small
public `octocat/Hello-World` repository. The worker image was already cached.

| Milestone | p50 | p95 and maximum |
| --- | ---: | ---: |
| Session accepted | 17 ms | 122 ms |
| Sandbox provisioning observed | 743 ms | 1,263 ms |
| Worker connected | 851 ms | 8,338 ms |
| Worker ready | 950 ms | 8,441 ms |
| Runtime running | 851 ms | 8,441 ms |
| Agent ready | 1,892 ms | 9,465 ms |
| Browser first command, separate run | 1,122 ms | 4,187 ms |

With five samples, the reported p95 is the maximum. These results verify the
local measurement path and expose variance. They do not establish production
percentiles.

### 8.2 What this baseline does not prove

- remote provider allocation or image transfer latency;
- a private or representative repository checkout;
- the production harness and remote credential path;
- desktop `terminal.first_frame` latency;
- image-cold host behavior;
- early browser scheduling on constrained shapes;
- correctness under checkout, restore, credential, or stream failure.

The local benchmark is a regression check. Hosted acceptance requires the
protocol in section 9.

## 9. Measurement and test protocol

### 9.1 Local regression run

Run at least five isolated cold Docker sessions. Reuse no session container.
The control plane may remain running, and the built image may be cached. Record
the cache condition explicitly.

The run must:

1. Start timing before the create request.
2. Capture every available milestone.
3. submit a follow-up message before workspace readiness;
4. verify that it executes after readiness and only once;
5. exercise a late browser command;
6. record failures rather than dropping samples;
7. tear down session containers after each run.

Add cases for a representative repository, checkout denial, bad credential,
restore failure, harness launch failure, stream disconnect, and browser launch
failure.

### 9.2 Checkout experiment matrix

For each checkout candidate, measure:

- time to workspace ready;
- transferred bytes;
- disk usage after checkout;
- first omitted-blob access;
- status, diff, blame, log, fetch, rebase, submodule, and large-file behavior;
- provider egress and request count;
- failure and retry behavior.

Run each cell enough times to separate protocol benefit from provider variance.
Do not promote a strategy solely from the smallest repository.

### 9.3 Hosted acceptance run

Collect at least 20 fresh sessions for each provider, region, shape, rootfs,
and worker image digest bucket used for a displayed estimate. Use the production
harness and a representative repository. Measure through the real desktop
renderer to `terminal.first_frame`.

Separate:

- cached-image hosts;
- image-cold hosts;
- fresh sessions;
- restored sessions;
- browser intent at create;
- late browser intent.

Report p50, p95, maximum, failure count, timeout count, image digest, repository
bucket, and provider dimensions. A failed sample remains in health reporting
and is never removed to improve latency percentiles.

### 9.4 Regression suites

Required checks for implementation:

- focused worker, transport, reconciler, store, and API tests;
- race tests for changed Go packages;
- frontend unit tests for pending routes, composer durability, focus, phase
  projection, and first-frame telemetry;
- cloud local smoke suite;
- local cold-start measurement;
- repository-wide backend and frontend checks required by CI;
- one real desktop run for the interaction and first-frame boundary;
- one supported remote-provider run before product estimates are enabled.

## 10. Failure semantics

| Failure | Durable state | User state | Retry behavior |
| --- | --- | --- | --- |
| Create rejected | no session | Saving session failed, text retained | correct input, retry create |
| Create response ambiguous | possibly committed | Saving session | replay idempotency key |
| Provider capacity | session and messages retained | Allocating workspace | bounded provider retry |
| Worker bootstrap | session retained | Starting workspace | replace worker epoch |
| Checkout grant denied | session and messages retained | Preparing repository failed | reconnect repository, retry |
| Clone or Git setup | session and messages retained | Preparing repository failed | retry fresh workspace |
| Restore | session and messages retained | Preparing repository failed | retry or explicit recovery |
| Credential | session and messages retained | Starting agent failed | reconnect credential, retry |
| Harness unavailable | session and messages retained | Starting agent failed | replace image or configuration |
| Harness launch | workspace retained where policy permits | Starting agent failed | retry launch or workspace |
| Terminal stream | durable turns retained | reconnecting terminal | redial, then polled fallback |
| Browser launch | agent unaffected | Starting browser failed | idempotent browser retry |

A checkout or restore failure must never launch the harness. A browser failure
must never terminate a healthy harness. Replacing a sandbox fences the previous
worker epoch before accepting new terminal work.

## 11. Resource and security limits

- No user compute exists before the create request.
- No sandbox is reassigned across sessions.
- Worker credentials remain scoped to the current session and worker epoch.
- Checkout credentials are requested by the worker and scoped to repository
  preparation.
- Early input buffers have explicit byte and message limits.
- Durable messages remain the recovery source after process loss.
- Terminal stream frames retain the existing maximum input size.
- Browserd remains loopback-only with capability authentication.
- Chromium starts only for explicit browser intent.
- Browser frames and terminal data never enter timing telemetry.
- Provider errors shown to users are bounded and redact secrets.

## 12. Expected file surface

The implementation is expected to touch these areas. Exact files may change as
the code is factored, but responsibility stays at these boundaries.

### Control plane and storage

- `cloud/internal/reconcile/reconciler.go`
- session create, resume, restore, and retry handlers
- sandbox intent store and notification paths
- worker work-wait notification path
- session event and timing projections

### Worker

- `cloud/cmd/ao-worker/main.go`
- `cloud/internal/worker/checkout.go`
- `cloud/internal/workertransport/supervisor.go`
- `cloud/internal/workertransport/stream.go`
- worker protocol and startup event types

### Renderer

- `frontend/src/renderer/components/TaskComposer.tsx`
- session lifecycle and pending-session state
- terminal pane and terminal mux integration
- `frontend/src/renderer/lib/cloud-orchestrator.ts`
- startup timing and telemetry helpers

### Validation

- local cloud smoke configuration
- `cloud/scripts/measure-cloud-cold-start.sh`
- focused Go and frontend tests
- hosted measurement runbook and result artifact

## 13. Implementation slices

### Slice 1: Correctness gates

- Replace ambiguous checkout completion with a typed result.
- Prevent launch after checkout or restore failure.
- Route stream and polled input through one admission path.
- Preserve initial prompt and follow-up message exactly-once ordering.
- Add cleanup and cancellation tests.

Exit condition: no harness can observe a partial workspace, and no terminal
path bypasses readiness.

### Slice 2: Event-driven startup

- Wake reconciliation on create, resume, restore, and retry.
- Wake the local work claim loop on readiness.
- Keep recovery polling.
- Add latency and race tests for both wake paths.

Exit condition: normal startup has no fixed two-second reconcile wait and no
post-readiness long-poll stall.

### Slice 3: Parallel worker preparation

- Fetch credentials beside checkout.
- Reserve terminal identity and buffering beside checkout.
- Join typed results before command construction.
- Prepare early browser intent on its independent lane.

Exit condition: milestone traces demonstrate real overlap, and all failure
combinations clean up correctly.

### Slice 4: Immediate interaction UI

- Add the pending session surface and stable composer.
- Hold pre-acknowledgement sends with truthful status.
- Bind the durable session without remounting the composer.
- Render startup phases and preserve focus.
- Unlock raw terminal input only after the first valid terminal boundary.

Exit condition: the user can type immediately, submit complete messages safely,
and never act on a partial checkout.

### Slice 5: Checkout and browser experiments

- Run the checkout strategy matrix.
- Measure early browser scheduling by shape.
- Select defaults only from representative evidence.

Exit condition: chosen optimizations preserve repository behavior and do not
regress user readiness.

### Slice 6: Hosted validation and estimates

- Run the hosted sample matrix.
- Validate the desktop first-frame event.
- Publish provider estimates only for qualified buckets.

Exit condition: displayed wait estimates are backed by at least 20 successful
samples in the exact bucket and accompanied by failure health.

## 14. Acceptance criteria

1. The pending session surface and editable composer appear within 100 ms at
   p95 on the reference desktop environment.
2. Text entered during create, allocation, checkout, and launch is never lost.
3. The initial prompt reaches the harness exactly once through launch context.
4. Follow-up messages are durable, ordered, and executed exactly once after
   readiness.
5. Raw terminal input from both persistent stream and polled transport is held
   behind the same readiness gate.
6. Checkout, credential, terminal reservation, and eligible browser work show
   actual overlap in milestone evidence.
7. Session creation wakes reconciliation without waiting for the periodic
   ticker.
8. Opening readiness causes the first pending claim within 250 ms at p95.
9. Checkout or restore failure never builds or launches the harness.
10. Restored files and transcript exist before command construction.
11. The hosted production path meets 15 seconds p50 and 45 seconds p95 from
    Cloud action to `terminal.first_frame` before those values are displayed.
12. Late browser intent reaches a command result or first keyframe within 3
    seconds at p95 on qualified shapes.
13. Early browser intent adds no browser-only wait after terminal readiness at
    p95.
14. No warm pool, standby worker, or pre-created user sandbox is introduced.
15. Chromium never starts without explicit browser intent.
16. Timing telemetry contains no prompts, messages, terminal data, repository
    contents, URLs, credentials, or browser frames.
17. Every injected failure displays the correct owning phase and a recoverable
    next action.
18. All focused tests, race checks, local smoke checks, frontend checks, and
    required CI suites pass.

## 15. Rollout

1. Land timing instrumentation and keep all product estimates hidden.
2. Land correctness gates and failure projection.
3. Land reconcile and worker wakeups behind existing recovery polling.
4. Land worker preparation concurrency and compare milestone traces.
5. Land the pending surface, durable composer flow, and phase UI.
6. Run local regression and checkout strategy matrices.
7. Run real desktop and remote-provider acceptance suites.
8. Enable provider-specific estimates only for qualified sample buckets.
9. Monitor failures, p50, p95, maximum, and phase dwell time by image digest.
10. Roll back individual optimizations without removing durable messaging or
    readiness gates.

## 16. Completion record

Implementation work is complete only when its handoff includes:

- changed files grouped by slice;
- exact test commands and observed results;
- local five-run timing output;
- hosted sample artifact or a stated hosted-validation gap;
- any checkout strategy decision and its evidence;
- any target miss with the owning phase identified;
- confirmation that no warm capacity or implicit browser startup was added.

## 17. Implementation progress, 2026-09-22

Completed in the first implementation slice:

- workspace preparation now returns an explicit success or failure result;
- checkout and restore failure cannot launch the harness;
- restore errors propagate instead of falling through to a fresh launch;
- credential fetch and terminal reservation begin while checkout is pending;
- a pre-reserved terminal receives the final harness command at launch;
- polled and streamed input use the same bounded readiness gate;
- buffered input flush is ordered before newly arriving live input;
- workspace and agent readiness wake the local work claim loop;
- sandbox intent commits notify a coalescing reconciler wake channel;
- the two-second reconcile ticker remains as recovery;
- focused tests, full Cloud tests, full Cloud race tests, vet, and build pass.

One fresh cached-image Docker run after these changes recorded:

| Milestone | Elapsed |
| --- | ---: |
| Session accepted | 14 ms |
| Sandbox provisioning observed | 124 ms |
| Worker connected | 230 ms |
| Worker ready | 230 ms |
| Runtime running | 230 ms |
| Agent ready | 1,161 ms |
| First browser command | 1,390 ms |

This single run is an integration result, not percentile evidence.

Completed in the final implementation and validation slice:

- the pending-session UI accepts input immediately and binds to the durable
  session without replacing the composer;
- startup milestones project into the pending surface and keep terminal input
  gated until a real output replay crosses the paint boundary;
- terminal replay frames survive listener-subscription races, and a reset-only
  replay cannot reveal an empty pane;
- five local Docker runs, lifecycle variations, failure ownership, transport
  fallbacks, lazy browser startup, and checkout candidates passed;
- the complete frontend unit and browser suites passed;
- an unlocked, focused native desktop run measured pending UI at 339 ms,
  durable route binding at 822 ms, and an uncovered input-ready terminal at
  1,751 ms.

Hosted validation remains open because this workspace has no supported
provider credentials or deployment context. No remote latency claim is made.

Completed in the click-triggered preparation follow-up:

- opening a Cloud task composer starts one hidden cold session immediately;
- Start Task commits that session and its first durable turn atomically;
- close, harness change, provider change, and server expiry reclaim abandoned
  preparations;
- normal session, child, orchestrator, and shared-project lists hide uncommitted
  preparations;
- credential and orchestrator preflight lookups run concurrently;
- known default branches use non-shallow, single-branch, blobless checkout with
  stale-metadata recovery;
- the final local lifecycle reached a running prepared harness in 3,392 ms and
  passed commit, cancellation, expiry, replacement, restart, and all transport
  modes.
