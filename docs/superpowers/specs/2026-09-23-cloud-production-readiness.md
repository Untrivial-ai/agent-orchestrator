> Historical combined-branch record. Preparation changes are now reviewed
> separately. For the current browser scope and test instructions, see
> [Shared browser sessions](../../cloud-shared-browser.md).

# Spec: Cloud production readiness and interaction quality

Status: reviewed, implementation in progress, 2026-09-23

Builds on:

- `docs/superpowers/specs/2026-09-21-cloud-cold-start-ux.md`
- `docs/superpowers/specs/2026-09-23-cloud-preparation-reconnect-grace.md`
- `docs/superpowers/specs/2026-09-23-cloud-browser-live-viewer.md`
- `cloud/internal/httpapi/browser_stream.go`
- `cloud/internal/vmbrowser/viewer.go`
- `frontend/src/renderer/lib/cloud-browser-stream.ts`
- `frontend/src/renderer/lib/cloud-session-preparation.ts`

This document is the implementation contract for the production-hardening
phase after the Cloud browser live viewer. It keeps cold-only allocation,
intent-driven Chromium, the direct non-durable browser stream, and durable
message queuing. It completes server-authoritative preparation reuse, cleanup
fencing, browser interaction quality, access controls, and production-shaped
regression coverage.

---

## 1. Outcome

The user can open New Task, type immediately, close and reopen the composer,
and interact with one shared remote browser without creating duplicate
workers or losing control state. A preparation that expires is fully cleaned
up, even when a provider responds after cancellation. Browser input has an
observable acknowledgement path and a precise relationship to the viewport
the user saw.

The user-facing startup sequence is:

```text
New Task opens
  -> Machine starting
  -> Terminal connected
  -> Repository downloading
  -> Workspace ready
  -> Task running
```

The composer accepts complete messages throughout this sequence. Messages are
stored durably and execute in order after workspace readiness. Raw terminal
keystrokes remain disabled until a real task terminal exists.

### 1.1 Product principles

1. New Task is the allocation boundary. No warm pool or speculative worker is
   created before the user opens a Cloud composer.
2. The server owns preparation identity, lease state, commit arbitration, and
   cleanup. Renderer state is a cache, not authority.
3. The remote Chromium is the shared source of truth for the user and task
   process.
4. Browser input acknowledgement confirms dispatch, not an assumed page
   result. Visual correlation is measured separately.
5. Clipboard transfer is explicit and one-shot. The system never watches or
   mirrors either clipboard continuously.
6. A late asynchronous result may reveal a resource that needs deletion, but
   it can never make an expired generation active again.

### 1.2 Non-goals

- Warm pools, shared standby workers, or preallocation before New Task.
- Starting Chromium based on prompt keywords.
- Running the task process against an empty or partial checkout.
- Raw CDP access from the desktop or control plane.
- Continuous clipboard synchronization, binary clipboard formats, files, or
  images.
- Multi-user simultaneous control in this phase.
- Persisting browser frames, browser input payloads, clipboard content, or a
  browser recording.
- Replacing the current direct browser stream with WebRTC.
- Redesigning the full terminal surface. This phase adds truthful basic
  readiness states and preserves queued message behavior.

## 2. Baseline and required change

| Area | Working baseline | Required completion |
| --- | --- | --- |
| Allocation | New Task starts fresh compute | Preserve cold-only behavior |
| Preparation reuse | Process-scoped compatibility registry with a two-minute lease | Server-side identity and deduplication across windows, renderer restarts, and devices |
| Preparation key | User, organization, project, harness, provider | Add checkout branch and every launch-time compute setting through a versioned server fingerprint |
| Commit | Atomic preparation commit with an idempotency key | Add claim generation and deterministic cross-window winner and loser behavior |
| Expiry | Session and sandbox move toward deletion | Fence every provider result and durably clean up resources returned after expiry |
| Browser startup | browserd connects early, Chromium starts on explicit intent | Preserve |
| Browser input | Ordered input sequence and basic dispatch acknowledgement | Add rejection outcomes, timeout behavior, deduplication, and viewport generation |
| Viewport | Debounced resize with input paused until acknowledgement | Bind frames and input to the accepted generation and coalesce resize bursts |
| Clipboard | Deferred | Add explicit text-only paste and copy-selection bridges |
| Viewer access | One-use, scoped ticket and current worker epoch checks | Remove credentials from URLs, narrow scopes, expose controller state, and tighten abuse limits |
| Reconnect | Automatic reconnect with last frame retained | Add explicit reconnecting and disconnected states, retry action, stale-frame treatment, and input fencing |
| Startup progress | Durable low-level milestones exist | Map them to five stable product states without invented progress |
| Regression | Unit suites, local Docker scripts, and a real desktop check exist | Add a repeatable production-shaped matrix for local, test provider, and staging provider paths |

## 3. Architecture decisions

### D1. Use a durable server claim for each compatible preparation

The current renderer registry remains a fast local cache. The Cloud API adds
an atomic lookup-or-create operation backed by a durable preparation claim.
Every compatible composer converges on that claim and its session.

This removes duplicate workers caused by multiple renderer windows, renderer
restarts, retries with different client request identifiers, or separate
devices signed into the same account.

### D2. Separate a preparation claim from composer attachments

A claim owns the worker preparation. A composer attachment records that one
window currently knows about the claim. Closing one window detaches only that
attachment and never cancels a claim another window may still use.

Attachments have short server leases and contain no prompt or attachment
content. Meaningful activity renews the attachment and the claim. Merely
leaving a window open does not renew either forever.

### D3. Commit and expiry are compare-and-swap transitions

Commit, expiry, and explicit cancellation compete on the claim generation.
Exactly one terminal transition wins. A request with an old generation cannot
renew, commit, or reveal the preparation.

### D4. Provider work is fenced by generation and operation identifier

Every create, start, stop, and delete operation carries:

```text
session_id
preparation_generation
provider_operation_id
```

When a result returns, the reconciler compares all three values with durable
state before applying it. A stale create success is recorded only as a cleanup
target and is deleted immediately. It never produces a worker credential,
marks the sandbox ready, or reveals the session.

### D5. Browser protocol acknowledgements describe transport facts

An accepted acknowledgement means browserd validated the command and the CDP
dispatch returned successfully. It does not claim that application code
handled the input or that the page visibly changed.

Frames carry a sequence and viewport generation. The renderer can therefore
measure dispatch acknowledgement and the first eligible painted frame as two
separate events.

### D6. Access and control are different capabilities

A viewer may be able to see frames without being allowed to operate the page.
Control is represented by an explicit capability and one current controller.
The UI never infers write access from a connected socket.

### D7. Clipboard transfer is user initiated

Paste reads text only from the actual desktop paste event. Copy occurs only
after an explicit copy action while the remote viewport is focused. No
background polling, history, automatic cross-device mirroring, or durable
storage is permitted.

### D8. Production tests use the normal product boundaries

The highest-value regression path uses the real worker image, control plane,
database, generated client, Electron application, browser relay, browserd,
and Chromium. Focused fakes remain useful for failure injection, but a fake
cannot satisfy the production-shaped release gate.

## 4. Preparation identity and reuse

### 4.1 Compatibility identity

The server creates a canonical compatibility document and hashes it. The
authenticated user and organization, provider connection, image revision, and
environment revision come from server state. The client sends only selectable
inputs such as project, checkout ref, harness, and launch configuration. The
server never accepts a client-provided compatibility hash. The first schema
version contains:

```json
{
  "version": 1,
  "organizationId": "org-id",
  "userId": "user-id",
  "projectId": "project-id",
  "checkoutRef": "refs/heads/main",
  "harness": "configured-harness",
  "sandboxProvider": "provider-kind",
  "providerConnectionId": "connection-id",
  "resourceProfile": "default",
  "workerImageRevision": "immutable-revision",
  "environmentRevision": "configuration-revision",
  "launchConfiguration": {
    "approvalMode": "configured-mode",
    "interface": "terminal",
    "model": "configured-model"
  }
}
```

Only values that affect allocation, checkout, the worker image, or the task
process at launch belong in the identity. Prompt text, display name, local
draft state, uploaded attachment contents, panel layout, and commit-time
settings do not belong in it.

The implementation review confirmed that model, effort, approval, and
interface selections are not currently forwarded to the Cloud worker launch.
Version 1 therefore omits them from the deployed hash. They enter a future
compatibility version only when the control plane consumes them before commit.

`checkoutRef` is the selected base branch or immutable revision after server
normalization. A generated task branch is not part of the identity because it
does not exist until commit. If a setting can be safely applied at commit, it
must stay outside the identity to maximize reuse.

The compatibility hash uses canonical JSON with sorted object keys, explicit
null handling, and SHA-256. The schema version is stored beside the hash so a
future identity change invalidates old claims without ambiguous comparison.

### 4.2 Durable model

The implementation may fit these fields into existing session and sandbox
records or add dedicated tables. The following invariants are required:

```text
preparation_claim
  id
  organization_id
  user_id
  project_id
  compatibility_version
  compatibility_hash
  session_id
  generation
  state
  lease_expires_at
  created_at
  updated_at

preparation_attachment
  claim_id
  client_instance_id
  lease_expires_at
  last_meaningful_activity_at
```

A partial unique constraint permits only one claim in `active` or
`committing` state for one organization, user, and compatibility hash.
For the current schema, the hidden session row is the claim and its session id
is the claim id. The sandbox row stores the generation. A separate attachment
table stores composer ownership and activity leases.
Creating a compatible preparation is one transaction:

1. Lock or insert the unique claim.
2. Return the existing live claim when present.
3. Otherwise create the hidden session, sandbox intent, and generation.
4. Upsert the calling composer attachment.
5. Return authoritative claim and attachment expiries.

The API response includes:

```json
{
  "disposition": "created",
  "claimId": "claim-id",
  "session": { "id": "session-id" },
  "preparation": {
    "expiresAt": "timestamp",
    "attachmentExpiresAt": "timestamp",
    "generation": 4
  }
}
```

`disposition` is `created` or `reused`. A reused response never starts a new
provider operation.

The existing API evolves without creating a second preparation workflow:

| Operation | Purpose |
| --- | --- |
| `POST /session-preparations` | Normalize identity, atomically look up or create the claim, and attach the composer instance |
| `POST /sessions/{sessionId}/renew-preparation` | Record meaningful activity for one client instance and return authoritative expiries |
| `DELETE /sessions/{sessionId}/preparation-attachments/{clientInstanceId}` | Detach one composer without canceling the shared claim |
| `POST /sessions/{sessionId}/commit-preparation` | Commit the referenced claim generation with one idempotency key |
| `DELETE /sessions/{sessionId}` | Perform the existing authorized cancel-all transition |

Every operation checks the authenticated user, organization, claim, session,
and generation relationship. A client instance identifier has no authority by
itself.

### 4.3 Composer behavior

1. Opening New Task creates a stable random `clientInstanceId` for that
   composer window and calls lookup-or-create immediately.
2. The renderer may show the composer before the response and accept draft
   text locally.
3. A compatible reopen calls the same endpoint and receives the live claim,
   including after an application restart.
4. Meaningful activity renews at most once per 45 seconds. Timers without
   activity do not renew.
5. Closing a composer detaches its attachment. The claim remains reusable
   until the server grace deadline.
6. Changing compatibility inputs attaches to a different claim. It does not
   cancel the old claim because another window may still use it.
7. Explicit sign-out, project deletion, organization removal, or an
   authorized cancel-all action cancels matching claims immediately.

### 4.4 Duplicate windows and commit races

Two compatible composers share one preparation before submission. Commit is a
compare-and-swap from `active` to `committing` using claim identifier,
generation, and a commit idempotency key.

- The winning request reveals the existing session and queues its first turn
  exactly once.
- A retry with the same idempotency key receives the same committed result.
- A losing request with a different idempotency key receives
  `PREPARATION_CONSUMED` and the committed session identifier, but it does not
  append its draft to that session.
- The losing composer preserves its draft. If the user still submits it, the
  client obtains a fresh preparation and commits that as a separate task.
- Closing or changing settings in one composer cannot delete the shared claim
  while another attachment or the reconnect grace remains valid.

### 4.5 Lease and reuse failure behavior

| Condition | Required response |
| --- | --- |
| Compatible claim is active | Reuse it and return `disposition: reused` |
| Claim expired before lookup | Fence it, schedule cleanup, create one replacement |
| Claim is committing | Return retryable `PREPARATION_COMMITTING` with bounded retry guidance |
| Claim was committed | Return `PREPARATION_CONSUMED`; never append another prompt |
| Compatibility changed | Use a new claim; let the old claim follow its own lease |
| Client loses a create response | Retry safely and resolve the same unique claim |
| Renderer process restarts | Lookup resolves the server claim from the compatibility identity |

## 5. Cancellation and cleanup

### 5.1 State model

```text
active
  -> committing
  -> committed

active
  -> cancellation_requested
  -> cleaning
  -> cleaned

committing
  -> committed
  -> cancellation_requested, only when commit transaction fails before reveal

cancellation_requested | cleaning
  -> cleaning, when a late provider resource is discovered
  -> cleaned, after provider confirms deletion or NotFound
```

Expiry is a reason for `cancellation_requested`, not a separate recoverable
state. Once the server clock finds the lease expired, renewal and commit for
that generation are permanently rejected.

### 5.2 Generation fence

Before a provider operation begins, the server persists its operation
identifier, generation, kind, and status. Applying a result requires the
current claim to match that generation and to permit that transition.

Provider create requests use the operation identifier as an idempotency token
when the provider supports one. They also tag the resource with the opaque
session identifier, generation, and operation identifier. Each provider
adapter must be able to recover a resource by operation identifier or list the
bounded tagged resources for that session. This closes the crash window where
the provider creates a resource but the control plane stops before persisting
the returned external identifier.

The current provider contract already recovers one resource by session id.
The create operation identifier is therefore deterministic as
`{session_id}:{generation}:create` and is placed in provider labels. Result
application checks the durable generation, while deletion recovery continues
to use the session correlation. A separate provider-operation table is not
required while one provider resource per session remains an invariant.

For a late create success:

1. Persist the returned external resource identifier on the provider
   operation or cleanup record.
2. Observe that the claim generation is stale or cancellation was requested.
3. Do not set sandbox state to ready.
4. Do not mint worker credentials or attach transport streams.
5. Enqueue provider deletion using the returned identifier.
6. Retry deletion with bounded backoff until success or provider NotFound.
7. Retain a durable cleanup failure with operator-visible diagnostics if the
   retry budget is exhausted.

The same rule applies when create returns after a replacement claim exists.
The old result is cleanup data only.

### 5.3 Cancellation triggers

- The claim grace period expires.
- The user performs an explicit cancel-all action.
- The user signs out and policy requires immediate resource release.
- The project or organization is removed.
- Preparation setup reaches an unrecoverable terminal failure.
- An operator cancels the session through an authorized administrative path.

Ordinary composer close only detaches. It does not cancel immediately.

### 5.4 Cleanup guarantees

1. Cleanup intent is committed before any delete call.
2. Reconciliation is level-triggered, so a process crash cannot lose cleanup.
3. Provider delete is idempotent. NotFound is success.
4. Session, sandbox, provider operation, and claim records retain enough
   sanitized data to prove the resource reached a terminal state.
5. Cleanup retries do not extend a user lease or make the preparation visible.
6. A terminal cleanup failure raises an alert with provider, region, resource
   type, age, operation identifier, and request identifier. It excludes
   credentials, repository URLs with secrets, prompt text, and clipboard data.
7. The reaper has an age-based sweep for orphaned operation records in
   addition to normal event-driven reconciliation.

### 5.5 Required race tests

- Expiry before provider create starts.
- Expiry while provider create is in flight.
- Provider success arriving before cancellation commit.
- Provider success arriving after cancellation commit.
- Commit and expiry on separate database connections.
- Two create retries from separate control-plane processes.
- Replacement generation created before the old generation returns.
- Delete timeout followed by success, NotFound, and permanent failure.
- Control-plane crash after receiving an external resource identifier but
  before delete begins.
- Worker callback from a fenced generation.

Every test asserts that at most one live provider resource exists for the
active claim and that no expired generation becomes visible or issues a
usable worker credential.

## 6. Input acknowledgement contract

### 6.1 Current baseline

Protocol version 1 already carries `inputSeq`, `accepted`, and `minFrameSeq`.
browserd sends an `input_ack` after CDP dispatch, and the renderer records
input-to-ack and input-to-frame measurements.

This phase makes the contract complete and user-visible. It does not replace
the direct stream or make input durable.

### 6.2 Input request

Every direct pointer, wheel, key, composition, or text input command carries:

```json
{
  "type": "input",
  "version": 2,
  "streamEpoch": 12,
  "inputSeq": 481,
  "viewportGeneration": 27,
  "action": { "kind": "pointer", "x": 412, "y": 188 }
}
```

Rules:

- `inputSeq` is strictly increasing within one stream epoch.
- `viewportGeneration` is required for coordinate-bearing, wheel, and key
  input because focus and layout belong to an accepted viewport.
- The relay preserves order and never acknowledges on behalf of browserd.
- The desktop does not automatically replay unacknowledged input after a
  reconnect. This prevents duplicate clicks and text.
- browserd keeps a bounded recent sequence cache for the active epoch. A
  duplicate sequence returns its prior outcome without reapplying input.

### 6.3 Acknowledgement and rejection

```json
{
  "type": "input_ack",
  "version": 2,
  "streamEpoch": 12,
  "inputSeq": 481,
  "status": "applied",
  "minFrameSeq": 9021,
  "viewportGeneration": 27
}
```

`status` is one of:

- `applied`: validation passed and CDP dispatch completed.
- `duplicate`: the same sequence was already applied and this is its cached
  outcome.
- `rejected`: input was not dispatched. A stable reason code is required.

Stable rejection reasons include `READ_ONLY`, `CONTROL_OWNED`,
`STALE_STREAM`, `STALE_VIEWPORT`, `RATE_LIMITED`, `INVALID_INPUT`,
`BROWSER_UNAVAILABLE`, and `SESSION_ENDED`.

`minFrameSeq` is the first frame sequence eligible to reflect the dispatch.
It is correlation metadata, not proof that the page changed. If Chromium
produces no changed frame, the acknowledgement still completes the input
operation.

### 6.4 Renderer behavior

1. Display a subtle pending-input indicator only when acknowledgement exceeds
   250 ms.
2. Clear it on `applied` or `duplicate`.
3. On `rejected`, show a short reason near the control-state badge and keep
   the browser frame stable.
4. At 2 seconds without acknowledgement, show `Input delayed` and stop
   accepting additional text input until the stream recovers.
5. At 5 seconds without acknowledgement, fence pending input, move to
   reconnecting, and never replay the old input automatically.
6. Keep at most 128 pending acknowledgements. Hitting the bound disables new
   input and reconnects instead of dropping an arbitrary command.
7. Record acknowledgement latency and first eligible painted-frame latency as
   separate histograms.

### 6.5 Release targets

| Measure | p50 | p95 | Failure ceiling |
| --- | ---: | ---: | ---: |
| User input to browserd acknowledgement | 90 ms | 300 ms | 2 s |
| User input to first eligible painted frame | 180 ms | 500 ms | 5 s |
| Agent input to first eligible painted frame | 200 ms | 600 ms | 5 s |

Hosted measurements report provider, region pair, frame dimensions, encoded
bytes, Chromium cold or warm state, and reconnect status. Local Docker and
hosted results are never combined into one percentile.

## 7. Browser access hardening

### 7.1 Transport and authentication

- Chromium debugging and browserd remain bound to loopback inside the worker.
- The worker opens the outbound stream. The control plane never dials a VM
  debugging port.
- Browser viewer traffic uses WSS outside isolated local development.
- The server validates the configured desktop origin and requested session
  path before accepting the WebSocket upgrade.
- The viewer WebSocket URL contains no ticket, capability, or credential.
- Immediately after the WebSocket opens, the first client frame must be an
  authentication frame containing the one-use ticket. The server permits no
  other message until redemption succeeds.
- An unauthenticated socket has a 3-second authentication deadline, an 8 KiB
  frame limit, and global plus source rate limits. Authentication failure uses
  one generic policy close and does not reveal whether a session exists.
- Ticket storage remains hash-only. Redemption is atomic and one-use.
- Tickets bind organization, user, session, worker epoch, permission scopes,
  expiration, and a random nonce.
- Session termination, sign-out, role change, and worker replacement revoke
  current tickets and connected viewer capabilities.

Moving the ticket out of the URL prevents it from appearing in routine proxy,
history, and request-path logs. Sanitizers still treat the authentication
frame as secret.

### 7.2 Permission scopes

The first implementation supports these independent scopes:

```text
browser:view
browser:operate
browser:clipboard-read
browser:clipboard-write
```

`browser:view` permits frames and non-sensitive browser state. It does not
permit input. `browser:operate` permits the allowlisted input and navigation
protocol. Clipboard scopes are separate because clipboard transfer crosses a
host boundary.

The server derives scopes from organization membership, session role, policy,
and current controller assignment. The renderer cannot request an escalation
beyond those facts.

### 7.3 Protocol boundaries

- Unknown message kinds close the stream with a policy error.
- Raw CDP method names and arbitrary script expressions are rejected.
- Every text message, binary frame, URL, text input, and clipboard payload has
  an explicit byte limit.
- URLs accept only the browser contract's supported schemes.
- Pointer coordinates and viewport sizes are range checked.
- Rate limits exist per socket, user, session, and organization.
- Frames, input bodies, and clipboard text are excluded from application logs,
  traces, durable events, and error payloads.
- Audit records contain actor, session, scope, action class, result, request
  identifier, and timestamp only.

### 7.4 Controller and read-only state

The attach response includes:

```json
{
  "viewerMode": "controlling",
  "controller": "user",
  "canRequestControl": true,
  "reason": null
}
```

`viewerMode` is `controlling` or `read_only`. Read-only responses include a
stable reason such as `ROLE`, `POLICY`, `OTHER_VIEWER`, `AGENT_ACTIVE`, or
`SESSION_ENDED`.

Only one desktop viewer controls a session. A second viewer starts read-only
and may request control when policy permits. Transfer is explicit and emits an
ordered control-state message. Disconnect releases user control after a short
grace, so a brief network interruption does not hand control to another
viewer unexpectedly.

## 8. Browser interaction quality

### 8.1 Dynamic viewport resizing

The existing `ResizeObserver` and debounce remain. The completed protocol
adds a monotonically increasing `viewportGeneration`.

```text
renderer measures 1180 x 742
  -> sends viewport generation 27
  -> browserd clamps and applies 1180 x 742
  -> sends viewport_ack generation 27 with accepted dimensions
  -> next frame carries generation 27
  -> renderer paints that frame and enables input for generation 27
```

Rules:

- Resize events are debounced at 100 to 150 ms.
- Only the latest unsent resize is retained.
- At most one viewport request is in flight. A newer desired size follows the
  current acknowledgement.
- Accepted dimensions are authoritative and included in every frame header.
- A stale-size frame may remain visible, dimmed, during resize.
- Coordinate-bearing input is disabled until a frame for the accepted
  generation is painted.
- The viewport range remains 320 by 240 through 1440 by 900 unless later
  measurements justify a change.
- Device scale factor remains 1 in this phase.

### 8.2 Clipboard synchronization

Clipboard behavior is text-only and explicit:

#### Paste into remote page

1. The controlling viewport receives a real paste event.
2. The renderer reads `text/plain` from that event only.
3. It sends at most 64 KiB of valid UTF-8 under
   `browser:clipboard-write`.
4. browserd inserts the text at the focused element.
5. The normal input acknowledgement contract reports success or rejection.

#### Copy selection to desktop

1. The user invokes Copy while the controlling viewport is focused or clicks
   `Copy selection` in the viewer menu.
2. browserd runs one fixed, reviewed selection extractor against the active
   page. Client-provided scripts are never accepted.
3. The result is capped at 64 KiB and returned under
   `browser:clipboard-read`.
4. A narrow desktop bridge writes the returned text to the operating-system
   clipboard.
5. The payload is discarded after delivery and never logged.

Clipboard requests carry a random request identifier. The response echoes the
identifier, direction, byte count, and `applied`, `rejected`, or `empty`
status. It never echoes clipboard text in an error.

The first slice supports ordinary document selection and selected text in
standard input and textarea elements. Site-specific transformed copy data,
HTML, images, files, and password-field extraction are unsupported. Copy from
password fields returns a policy rejection.

Cut first obtains the same bounded selection, then dispatches the remote cut
shortcut. The desktop clipboard changes only after browserd acknowledges the
remote dispatch. If selection extraction is unsupported, the shortcut remains
remote-only and the UI does not overwrite the desktop clipboard.

Organization policy may disable either clipboard direction. The UI then
shows the viewer as operational with clipboard unavailable, rather than
failing the whole stream.

### 8.3 Scrolling

- Normalize DOM wheel delta modes to CSS pixels.
- Preserve horizontal and vertical deltas.
- Coalesce only adjacent wheel events with the same modifier set and target
  generation, for no longer than one animation frame.
- Flush accumulated wheel input before a pointer down, key event, viewport
  change, ownership change, or disconnect.
- Do not turn trackpad motion into fixed notches.
- Touchpad pinch remains unsupported and must not be mistaken for page zoom.
- Page zoom commands are explicit toolbar or shortcut actions with bounded
  values.

### 8.4 Keyboard shortcuts and composition

The viewer handles shortcuts by category:

| Category | Examples | Behavior |
| --- | --- | --- |
| Page editing | select all, copy, paste, cut, undo, redo | Forward or use the clipboard bridge while the remote viewport is focused |
| Remote navigation | reload, back, forward | Translate to allowlisted viewer actions |
| AO application | command palette, session switching, settings | Keep local and never forward |
| Operating system | quit, hide, secure attention sequences | Keep local and never forward |
| Ordinary key and composition | text, arrows, Tab, Enter, Escape, input method composition | Forward through the ordered input protocol |

The implementation uses physical key code, logical key, location, repeat,
modifier state, and composition messages. It must test Windows, macOS, and
Linux modifier mapping even when CI executes on fewer operating systems.
Key-up events are forwarded for every forwarded key-down event unless the
stream disconnects, in which case browserd releases all held modifiers.

### 8.5 Visible focus state

The remote viewport has an always-visible focus ring when it owns keyboard
focus. A short label says `Browser input active` on focus entry and disappears
after 1.5 seconds while the ring remains. When focus leaves, the ring and label
clear immediately.

Read-only, resizing, reconnecting, and disconnected states use distinct
visual treatment and cannot show the active-input ring. Pointer click focuses
the viewport only if the viewer can operate. Tab navigation can reach and
leave the viewport without trapping the user.

### 8.6 Reconnecting and disconnected indicators

Viewer connection states are:

```text
connecting
waiting_for_browser
ready
reconnecting
disconnected
ended
```

- `reconnecting` retains the last frame with a dimmed `Reconnecting` overlay,
  disables input, and shows the attempt count.
- Automatic retry uses bounded exponential backoff with jitter.
- After 30 seconds or the retry budget is exhausted, state becomes
  `disconnected` and presents `Retry connection`.
- A manual retry starts a fresh ticket and stream epoch.
- `ended` is terminal and never retries.
- Pending input from an old stream epoch is fenced and never replayed.
- A successful reconnect requests a fresh full frame before enabling input.

### 8.7 Explicit control state

The viewer always shows one compact state badge:

- `You control`
- `Task controls`
- `Read-only`
- `Requesting control`
- `Disconnected`

The badge has accessible text and exposes the reason on activation or hover.
Toolbar buttons, pointer input, wheel input, keyboard input, clipboard actions,
and navigation all derive enablement from the same control-state object. No
component maintains an independent guess.

## 9. Terminal before checkout completion

This phase implements only the basic truthful surface. It does not create a
shell before the workspace is safe.

### 9.1 Stable progress mapping

| Product state | Starts when | Completes when | User action |
| --- | --- | --- | --- |
| Machine starting | Preparation claim is created | Current worker epoch connects | Compose and submit complete messages |
| Terminal connected | Worker terminal transport registers | Checkout starts or an empty workspace is confirmed | Compose and submit complete messages |
| Repository downloading | Checkout milestone starts | Checkout and restore complete | Compose and submit complete messages |
| Workspace ready | Workspace-ready milestone is durable | Task process launch starts | Messages remain queued in order |
| Task running | Task process is ready and first terminal frame is available | Session ends | Raw terminal interaction and messages |

If independent steps overlap, the UI advances only when the listed durable
milestone occurs. It never uses a timer or estimated percentage as truth.

### 9.2 Input behavior

- The ordinary message composer is enabled immediately.
- Every submission receives a local pending state, then a durable server
  acknowledgement, then queued, delivered, and running states where
  available.
- Messages preserve submission order across reconnect and application restart.
- Raw PTY input, terminal resize escape handling, and control keys remain
  locked until `Task running`.
- The terminal panel may mount early and display the five progress states, but
  it must not resemble an interactive shell before a PTY exists.
- Checkout failure preserves drafted and durable messages, shows the failed
  step, and offers retry or cancel.

## 10. Telemetry, privacy, and operations

### 10.1 Required measurements

- New Task click to claim response.
- Claim disposition, created or reused.
- Claim response to worker connected.
- Worker connected to checkout start and completion.
- Checkout completion to task process ready.
- Browser attach to first frame, split by Chromium cold or running.
- Input capture to browserd acknowledgement.
- Acknowledgement to first eligible painted frame.
- Viewer reconnect detection to fresh frame.
- Preparation expiry to provider deletion confirmation.
- Count and age of cleanup records with unresolved provider resources.
- Deduplication count across composer instances.

### 10.2 Privacy rules

Telemetry may contain opaque organization, project, session, claim, provider,
region, generation, operation, and request identifiers. It must not contain
prompt text, terminal input, browser URLs with query strings, page titles,
frame bytes, typed text, clipboard contents, repository credentials, or viewer
tickets.

### 10.3 Operational alerts

Alert on:

- cleanup not confirmed within 10 minutes of cancellation;
- more than one live provider resource for one active claim;
- a fenced generation issuing a worker credential;
- sustained input acknowledgement p95 above 300 ms;
- viewer reconnect failure rate above the release baseline;
- ticket redemption replay or cross-session mismatch;
- active preparation count exceeding the user and organization quota model.

## 11. Production-shaped regression matrix

### 11.1 Test layers

| Layer | Runtime boundary | Purpose | Release use |
| --- | --- | --- | --- |
| Unit | Package-local fakes and deterministic clocks | State transitions, validation, serialization, input mapping | Every change |
| Integration | Real Postgres, HTTP server, WebSockets, and Chromium fixture page | Transactions, races, protocol, browser dispatch | Every change |
| Local Docker | Built worker image, control plane, Postgres, browserd, Chromium | Full cold lifecycle and failure injection | Every Cloud PR |
| Real desktop | Isolated Electron data and normal API path | Focus, keyboard, clipboard, resize, reconnect, progress UI | Every visible viewer change |
| Coder test provider | Fresh provider workspace from production-shaped template | Hosted allocation, routing, credentials, cleanup, latency | Nightly and release candidate |
| NodeOps staging | Fresh live sandbox through the production provider adapter | Final provider contract and regional behavior | Before production rollout |

Every provider scenario is cold. The test first asserts that no sandbox or
provider resource identifier already exists for the new claim. Ordinary image
and registry caches are allowed and reported, because production uses them.

### 11.2 Coverage matrix

| Scenario | Unit | Integration | Docker | Desktop | Coder | NodeOps |
| --- | :---: | :---: | :---: | :---: | :---: | :---: |
| New Task starts one preparation | X | X | X | X | X | X |
| Duplicate composer windows reuse it | X | X | X | X | X | X |
| Close and reopen within grace | X | X | X | X | X | X |
| Open and idle through expiry | X | X | X | X | X | X |
| Branch or launch configuration changes | X | X | X | X |  |  |
| Simultaneous commit race | X | X | X | X |  |  |
| Expiry during provider create | X | X | X |  | X | X |
| Late create result is deleted | X | X | X |  | X | X |
| Checkout failure and retry | X | X | X | X | X | X |
| No browser intent keeps Chromium off | X | X | X | X | X | X |
| Early and late browser intent | X | X | X | X | X | X |
| Pointer, typing, scroll, and composition | X | X | X | X | X | X |
| Shortcut allowlist and local reservation | X |  | X | X |  |  |
| Clipboard scopes and text limits | X | X | X | X | X | X |
| Resize during active input | X | X | X | X | X | X |
| Read-only viewer and control transfer | X | X | X | X | X | X |
| Viewer reconnect and stale input fence | X | X | X | X | X | X |
| Chromium crash and restart | X | X | X | X | X | X |
| Worker replacement and stale epoch | X | X | X | X | X | X |
| Slow viewer backpressure | X | X | X |  | X | X |
| Expired, replayed, and wrong-scope ticket | X | X | X |  | X | X |
| Session termination cleanup | X | X | X | X | X | X |

Blank hosted cells are deliberate because the property is fully covered at a
lower deterministic boundary and does not justify provider cost in each run.

### 11.3 Production-shaped local scenario

The local end-to-end runner must:

1. Build the actual worker image used by the local Cloud stack.
2. Start isolated Postgres and the control plane with normal migrations.
3. Create the project and task through public application APIs.
4. Open two compatible preparation requests with distinct composer instance
   identifiers and assert one claim, session, worker, and provider resource.
5. Close both attachments, reopen one within grace, and assert reuse.
6. Submit a message before readiness and verify its durable acknowledgement and
   ordered delivery after checkout.
7. Attach the browser viewer, prove Chromium was previously absent, and wait
   for the first frame.
8. Exercise viewport generation, pointer, text, wheel, composition, shortcuts,
   copy selection, paste, and read-only rejection.
9. Interrupt the viewer, Chromium, worker stream, and control plane in separate
   cases, then verify recovery and stale input fencing.
10. Expire a preparation while provider creation is delayed, release the late
    result, and verify deletion plus absence of a usable worker credential.
11. Terminate the committed session and assert database terminal state,
    provider NotFound, no active relay, and no orphan container or volume.
12. Emit a sanitized JSON report with timings, resource counts, provider
    metadata, image revision, protocol version, and pass or fail results.

No direct database mutation may create the happy path. Targeted failure
injection may pause provider responses or clocks, but user actions and normal
transitions still cross public product boundaries.

### 11.4 Real desktop scenario

Run the actual Electron application with a scratch data directory and a real
provider catalog. The test or reviewer performs:

1. Open New Task and verify immediate typing plus `Machine starting`.
2. Open a duplicate composer and verify both resolve the same preparation.
3. Close and reopen, then verify the same session remains.
4. Submit before checkout finishes and observe each truthful progress state.
5. Open Browser and verify first frame, visible focus, controlling badge, and
   dynamic resize.
6. Verify pointer, trackpad scroll, ordinary keys, composition, approved
   shortcuts, one-shot copy, and one-shot paste.
7. Open a second viewer and verify explicit read-only state.
8. Disconnect the stream and verify reconnecting, disconnected, retry, and
   fresh-frame behavior.
9. End the task and verify the viewer becomes terminal rather than retrying.

Visible changes require a screenshot and, where interaction matters, a short
recording from this real application path for review evidence.

### 11.5 Regression commands

The implementation plan may add focused commands, but the release candidate
must pass at least:

```bash
cd cloud && go test ./...
cd cloud && go test -race ./...
cd cloud && go vet ./...
cd cloud && go build ./...
npm --prefix packages/cloud-client run generate
npm --prefix packages/cloud-client run typecheck
npm --prefix packages/cloud-client test
npm --prefix packages/cloud-client run build
npm --prefix frontend test
npm --prefix frontend run typecheck
npm --prefix frontend run build
npm --prefix frontend run test:e2e
npm run shared:check
cloud/scripts/test-cloud-cold-start-variations.sh --local
cloud/scripts/test-cloud-browser-viewer.sh --local
```

Generated output must be clean after regeneration. Provider suites publish
separate Coder and NodeOps reports and do not treat local Docker timing as a
hosted result.

## 12. Fault injection contract

Production-shaped tests need bounded hooks available only in test builds or
with an explicit test-only server configuration:

- pause provider create before request, after request, and before result
  application;
- return provider success after the request context is canceled;
- delay or drop one input acknowledgement;
- close viewer or worker WebSockets after a selected sequence;
- pause checkout at started and completed boundaries;
- crash Chromium after a selected frame;
- slow a viewer reader without slowing browserd;
- force delete timeout, NotFound, and permanent failure.

Hooks must be impossible to enable in production by ordinary API input. The
server rejects test headers or query fields unless the process started with
the dedicated test configuration.

## 13. Implementation slices

### Slice 1: Server preparation claim and cleanup fence

- Add canonical compatibility calculation and durable claim uniqueness.
- Add composer attachments and server lookup-or-create.
- Add generation-aware renew, commit, expiry, and cancellation.
- Persist provider operation identifiers and late-result cleanup.
- Add database race and reaper tests.

Exit gate: duplicate clients produce one worker, and every delayed create
result is either the active matching generation or a durably tracked deletion
target.

### Slice 2: Input acknowledgement and access hardening

- Version the browser protocol.
- Add acknowledgement outcome, viewport generation, duplicate sequence
  handling, and client timeout behavior.
- Move viewer ticket redemption to the first socket frame.
- Add explicit scopes, revocation, limits, and control-state messages.

Exit gate: authorized input is acknowledged once, stale or unauthorized input
is rejected with a stable reason, and no viewer secret appears in a URL.

### Slice 3: Browser interaction quality

- Complete viewport generation and resize coalescing.
- Add text-only clipboard bridges and policy.
- Normalize scrolling and implement the shortcut routing table.
- Add visible focus, reconnect, disconnected, and controller states.
- Verify with the real desktop application.

Exit gate: every interaction in the desktop scenario works against one shared
Chromium and all disabled states reject input at both UI and server boundaries.

### Slice 4: Basic startup progress

- Map existing durable milestones to the five product states.
- Show message acknowledgement and queued status before task readiness.
- Keep raw PTY behavior behind the existing readiness gate.

Exit gate: the UI never displays a later state before its durable milestone,
and a message submitted during machine startup runs once after workspace
readiness.

### Slice 5: Production-shaped regression and rollout

- Extend local Docker coverage and sanitized reporting.
- Add the isolated desktop checklist or automation.
- Add scheduled Coder coverage and pre-release NodeOps coverage.
- Add alert queries and release dashboards.

Exit gate: the complete release matrix passes with zero leaked provider
resources and published timing reports separated by provider.

## 14. Rollout and rollback

Use independent server-controlled flags:

```text
cloud_preparation_claims_v1
cloud_browser_protocol_v2
cloud_browser_clipboard_text
cloud_browser_control_state_v1
cloud_startup_progress_v1
```

Rollout order follows the implementation slices. Preparation claims start with
internal accounts, then a small organization cohort, then all new
preparations. Existing version 1 viewer streams remain supported during one
desktop release overlap, but a stream never mixes protocol versions.

Clipboard starts disabled by default, then enables for internal accounts and
explicit organizations after security review. Disabling clipboard does not
disable the viewer.

Rollback rules:

- Disabling preparation claims stops new claim creation but leaves the reaper
  and cleanup reconciler active until all claims are terminal.
- Disabling browser protocol version 2 reconnects clients using version 1
  during the compatibility window.
- Disabling clipboard removes its scopes from newly issued tickets and revokes
  clipboard operations on connected streams.
- Disabling startup progress falls back to the existing pending session UI.
- No rollback may skip provider cleanup or accept an old generation.

## 15. Release gates

The phase is complete only when all gates pass:

1. The compatibility identity is server-derived, versioned, and covered by
   canonicalization tests.
2. Duplicate windows, processes, and retries create exactly one active
   preparation for a compatible key.
3. Commit, expiry, and cancellation races are deterministic under database
   concurrency tests.
4. A late provider success cannot reveal, authenticate, or reconnect an
   abandoned generation.
5. Cleanup reaches provider deletion or NotFound in every automated failure
   scenario, with no untracked orphan.
6. Input acknowledgement, viewport generation, rejection, and reconnect
   behavior pass against real Chromium.
7. Viewer secrets never appear in URLs or logs, and scope tests cover view,
   operate, and both clipboard directions.
8. Resize, scroll, keyboard, composition, clipboard, focus, read-only,
   reconnect, and disconnected states pass in the real desktop application.
9. The five startup states come only from durable milestones, and early
   messages execute exactly once in order.
10. Full Cloud, frontend, shared-package, race, static-analysis, build, and
    generated-client checks pass.
11. The local Docker production-shaped suite passes from a clean environment
    and removes all test resources.
12. Coder test-provider and NodeOps staging reports pass separately before
    production rollout. If either provider is unavailable, the phase remains
    unverified for that provider and cannot be described as fully validated.

## 16. Risks and tradeoffs

### Server deduplication adds state

This is deliberate. Renderer-only deduplication cannot protect against a
second process, device, or retry routed to another control-plane instance. A
small durable claim and attachment model is less costly than duplicate
provider resources and ambiguous task ownership.

### Acknowledgement is not page success

CDP dispatch can succeed while page code ignores or prevents an event. The UI
must say input was sent, not that a button definitely completed its action.
Snapshots, page state, and later frames remain the source of outcome truth.

### Clipboard copy has limited fidelity

A fixed selection extractor is safer than exposing arbitrary evaluation. Some
applications implement custom copy behavior that the first slice will not
reproduce. The UI should label the feature `Copy selection`, not imply full
remote clipboard equivalence.

### Early terminal appearance can imply readiness

The terminal panel must use progress presentation until the task PTY exists.
Giving it a prompt or caret early would invite raw keystrokes that cannot be
replayed safely.

### Hosted testing costs time and provider capacity

Provider scenarios are required for release confidence but do not need to run
on every small commit. Deterministic unit, integration, and local Docker suites
run per change; hosted suites run nightly and for release candidates.
