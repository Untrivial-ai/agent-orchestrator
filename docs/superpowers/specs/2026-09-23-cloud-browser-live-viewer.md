# Spec: Cloud browser live viewer and shared control

Status: implemented and locally validated, 2026-09-23

Builds on:

- `docs/superpowers/specs/2026-09-17-cloud-browser.md`
- `docs/superpowers/specs/2026-09-21-cloud-cold-start-ux.md`
- `docs/cloud-browser-stage1.md`
- `cloud/cmd/ao-worker/browserd.go`
- `cloud/internal/vmbrowser/`
- `cloud/internal/httpapi/terminal_stream.go`
- `cloud/internal/httpapi/terminal_handlers.go`
- `frontend/src/renderer/components/BrowserPanel.tsx`
- `frontend/src/renderer/hooks/useBrowserView.ts`

Protocol references:

- [Chrome DevTools Protocol, Page domain](https://chromedevtools.github.io/devtools-protocol/tot/Page/)
- [Chrome DevTools Protocol, Input domain](https://chromedevtools.github.io/devtools-protocol/tot/Input/)
- [Chrome DevTools Protocol, Target domain](https://chromedevtools.github.io/devtools-protocol/tot/Target/)
- [Chrome DevTools Protocol, Emulation domain](https://chromedevtools.github.io/devtools-protocol/tot/Emulation/)

This document is the implementation contract for Stage 2. It supersedes the
Stage 2 transport details in the 2026-09-17 browser spec. Stage 1 automation
and its loopback security boundary remain unchanged.

## 1. Product outcome

One Cloud session owns one Chromium profile and one set of tabs. The coding
agent and the user operate that same browser.

The required experience is:

```text
Agent clicks, types, scrolls, or navigates
  -> the shared Chromium changes
  -> the next browser frame shows the change to the user

User clicks, types, scrolls, or navigates
  -> input reaches the same Chromium
  -> the page changes
  -> the agent's next snapshot or browser query sees that state
```

There is no second browser, DOM copy, or replayed simulation. Chromium is the
shared source of truth.

### 1.1 User timeline

```text
User opens the Browser tab
  -> viewer shell renders immediately
  -> client mints a one-use viewer ticket and opens a WebSocket
  -> control plane tells the already-connected worker to attach the viewer
  -> browserd starts Chromium if it is cold
  -> first full frame renders
  -> clicks, typing, wheel input, and navigation are enabled

Agent performs a browser action
  -> viewer shows "Agent controlling"
  -> action runs against the active tab
  -> changed frame renders without a page refresh

User interacts
  -> viewer shows "You are controlling"
  -> direct input is dispatched to the active tab
  -> agent mutations wait briefly instead of racing the user
```

### 1.2 Latency targets

All targets are measured from the desktop client on a regional connection.
They are release gates, not claims about current hosted performance.

| Path | p50 | p95 | Measurement |
| --- | ---: | ---: | --- |
| Open viewer, browser already running | 350 ms | 1.0 s | Browser tab selected to first frame painted |
| Open viewer, Chromium cold | 2.0 s | 5.0 s | Browser tab selected to first frame painted |
| Agent input becomes visible | 200 ms | 600 ms | browserd dispatch to first correlated changed frame painted |
| User input becomes visible | 180 ms | 500 ms | Input captured to first correlated changed frame painted |
| Viewer reconnect | 500 ms | 1.5 s | Socket loss detected to replacement frame painted |

Measurements must report provider, region pair, viewport, frame bytes, codec,
and whether Chromium was already running. Local Docker numbers and hosted
numbers are reported separately.

## 2. Goals and non-goals

### 2.1 Goals

1. Show the active Cloud Chromium tab in the desktop Browser panel.
2. Show browser actions as they happen, without polling screenshots.
3. Send user pointer, wheel, keyboard, text, and navigation input back to the
   same active tab.
4. Keep frame and input latency independent of the durable worker request
   queue.
5. Bound memory and bandwidth when the page changes quickly or a viewer is
   slow.
6. Preserve Stage 1 automation behavior when no viewer is attached.
7. Keep Chromium and its debugging endpoint loopback-only inside the VM.
8. Recover from viewer, worker, and Chromium reconnects without replacing the
   session profile.

### 2.2 Non-goals

- Audio or video playback transport.
- Full DevTools UI.
- File upload, drag-and-drop from the desktop, clipboard sync, or download
  transfer.
- Network capture.
- Native browser dialogs such as OS file pickers and print preview.
- Multi-user collaborative control. Stage 2 has one controlling desktop
  connection per session.
- Persisted video, screenshots, input events, or a browser session recording.
- WebRTC. It remains an upgrade path if measured screencast performance misses
  the release targets after adaptive quality is implemented.
- Starting Chromium when neither the agent nor the user expresses browser
  intent.

## 3. Existing foundation

Stage 1 already provides:

- Chromium and the pinned browser engine in the worker image.
- Lazy Chromium startup through `vmbrowser.Chromium.EnsureRunning`.
- A per-session profile and loopback-only debugging endpoint.
- `browserd`, which speaks the shared browser command contract and validates a
  per-session capability.
- Full in-VM browser verbs except the documented unsupported set.
- Idle shutdown, crash restart, and restart backoff.

The Cloud terminal already proves the useful streaming shape:

- the worker opens an outbound authenticated WebSocket;
- the browser client opens a ticket-authenticated WebSocket;
- the control plane joins the two live directions in memory;
- bounded channels prevent a slow client from blocking the producer.

Browser media differs from terminal output in one important way. Terminal
bytes require ordered durable replay. Browser frames are disposable snapshots.
An old frame may be dropped whenever a newer frame exists.

## 4. Decisions

### D1. Use the same Chromium, profile, and active tab

The viewer attaches a second CDP session to the Chromium already driven by the
Stage 1 engine. The viewer never launches a second Chromium and never loads the
page in a desktop iframe.

The automation session and viewer session have separate CDP domain state, but
they share browser targets, cookies, storage, navigation, focus, and DOM state.

### D2. Use a direct live data plane

Frames, pointer events, key events, text, and wheel events use persistent
WebSockets:

```text
Desktop viewer WebSocket
  <-> control-plane browser relay
  <-> worker browser WebSocket
  <-> browserd loopback WebSocket
  <-> Chromium CDP
```

These messages never enter `ao_worker_requests`, webhook delivery, the client
event log, terminal history, or another durable table. The existing durable
queue remains for workspace operations and the current proxy fallback only.

The worker browser socket connects after worker bootstrap and stays idle until
an attach message arrives. An idle socket does not start Chromium and does not
count as user activity.

### D3. Viewer attach is explicit browser intent

Chromium starts when any of these happens:

1. an in-VM browser verb reaches browserd;
2. a user opens the Browser tab and the viewer socket attaches;
3. session metadata explicitly requests early browser preparation.

Opening a session, typing a prompt, or matching words in a prompt does not
start Chromium.

### D4. browserd owns CDP and exposes an allowlisted viewer protocol

The worker does not expose or forward raw CDP. browserd gains a
capability-authenticated loopback viewer WebSocket. It translates a small AO
protocol into these CDP operations:

- `Page.startScreencast`, `Page.screencastFrame`,
  `Page.screencastFrameAck`, and `Page.stopScreencast`;
- `Input.dispatchMouseEvent`, `Input.dispatchKeyEvent`, `Input.insertText`,
  and composition handling;
- `Emulation.setDeviceMetricsOverride` for the shared viewport;
- allowlisted target listing, activation, creation, and close operations;
- navigation, reload, back, forward, and JavaScript dialog handling.

Unknown message kinds, unknown CDP-equivalent operations, and oversized fields
close the local stream with a policy error.

### D5. Latest frame wins at every boundary

Frame buffers have capacity one per active viewer. When a newer frame arrives,
it replaces an unsent older frame. Control state messages remain ordered and
use their own small bounded queue.

browserd acknowledges a CDP screencast frame after it has copied the frame into
its bounded latest-frame slot. It does not wait for the desktop to render the
frame. This prevents a slow or disconnected desktop from blocking Chromium.

The relay never retries or persists a frame. Reconnect requests a fresh full
frame.

### D6. JPEG screencast first, adaptive within fixed bounds

Initial settings:

- JPEG quality 70;
- maximum 1440 by 900 CSS pixels;
- device scale factor 1;
- maximum 15 frames per second;
- no frame when Chromium reports no visual change;
- maximum encoded frame size 1 MiB.

If a frame exceeds 1 MiB, browserd lowers quality, then dimensions, and emits
the chosen settings in viewer state. It never splits one image across durable
messages. If render delay or send saturation stays above 500 ms for three
seconds, the stream steps down in this order: 15 to 10 to 6 frames per second,
then 70 to 55 quality, then 1440 by 900 to 1280 by 720. It steps up only after
ten stable seconds.

### D7. Browser panel size defines the shared viewport

The desktop sends a debounced viewport message after attach and resize. The
accepted range is 320 by 240 through 1440 by 900. browserd applies the size to
the active target and returns the accepted size.

The viewer maps pointer coordinates against the accepted frame dimensions,
not the current CSS box. Letterboxing is excluded from the hit area. A stale
frame remains scaled for display during resize, but input stays disabled until
the new viewport state is acknowledged.

This means the agent snapshot and user view use the same responsive layout.

### D8. browserd arbitrates the two input writers

Read-only agent operations may run while the user controls the page. Mutating
operations have one owner at a time.

Read-only operations are:

- status, snapshot, get, tabs, wait, console, errors, screenshot, and dialog
  status.

Every other browser verb is mutating for arbitration purposes.

Rules:

1. An agent mutation owns control until that command returns.
2. While an agent mutation owns control, user input is rejected, never queued.
   The UI shows `Agent controlling`.
3. A user pointer-down, wheel, key, text, navigation, or tab operation acquires
   a rolling 1.5 second user lease. Each accepted user event renews it.
4. An agent mutation waits up to 3 seconds for the user lease to expire. If the
   user remains active, it fails with `BROWSER_USER_CONTROL_ACTIVE` and may be
   retried by the caller.
5. Pointer moves without a pressed button do not acquire or renew the lease.
6. Disconnect releases user control immediately.
7. Browser crash or target replacement resets ownership to idle.

The viewer receives owner state and never pretends an input was accepted. The
agent's next snapshot or query reads the page after accepted user input, so no
parallel DOM synchronization is needed.

### D9. One desktop stream controls a session

The first valid desktop viewer becomes the controller. A reconnect from the
same signed-in principal replaces its older socket. A second principal is
rejected with `BROWSER_VIEWER_IN_USE` in Stage 2. Observer and collaborative
control modes require a separate product decision.

Within the Electron app, Browser panel, inspector, and pop-out surfaces share
one renderer-side stream object. React remounts must not create competing
server sockets.

### D10. Tickets and worker epochs fence every connection

The desktop requests a random, hashed-at-rest, single-use browser viewer
ticket. The ticket is bound to organization, session, user, worker epoch,
purpose `browser-view`, and a five-minute redemption window. Successful
redemption consumes it.

The worker stream uses the existing worker credential and requires
`worker:transport`. A newer worker epoch closes the older browser stream. No
browser capability or worker credential reaches the renderer.

### D11. Stage 2 uses the current fixed relay replica

Production currently runs one fixed control-plane replica because the terminal
relay has process-local affinity. Stage 2 may use the same constraint for its
first canary.

Scaling the control plane beyond one replica is blocked until both worker and
viewer browser sockets are routed to the same relay shard, or the relay moves
to a dedicated service. Frames must not use PostgreSQL, notifications, or a
durable queue as a cross-replica fallback.

### D12. The proxy viewer is a fallback, not an automatic downgrade

The existing fetch-and-rewrite browser route stays available behind its own
flag during rollout. A live viewer failure shows a reconnect or explicit error
state. It does not silently switch to the proxy because that would show a
different browser state and break the shared-control guarantee.

## 5. Component architecture

```text
Electron renderer
  BrowserPanel cloud surface
    canvas, focus sink, input mapper, shared stream client
        |
        | one-use ticket, WSS
        v
Cloud control plane
  ticket issuer
  browser relay hub
    one worker socket + one viewer socket per session
    latest-frame slots, ordered control queues, no persistence
        ^
        | worker credential, WSS opened outbound from VM
        |
Session worker
  browser stream client
    reconnect, attach/detach forwarding, loopback bridge
        |
        | session capability, loopback WSS
        v
browserd
  viewer controller
    CDP screencast, input dispatch, viewport, target state, arbiter
  Stage 1 command service
    agent browser verbs
        |
        v
One headless Chromium, one profile, shared targets
```

### 5.1 Ownership boundaries

| Component | Owns | Does not own |
| --- | --- | --- |
| Renderer | paint, focus, coordinate mapping, input capture, reconnect UX | credentials, CDP methods, frame persistence |
| Control plane | user authorization, ticket redemption, socket pairing, bounds | browser state, image decoding, durable media |
| Worker | outbound connectivity, epoch fencing, browserd bridge | input policy, browser target state |
| browserd | Chromium lifecycle, CDP sessions, active target, arbitration | tenant authorization, public sockets |
| Chromium | page, profile, tabs, cookies, DOM, focus | Cloud identity or session policy |

## 6. Public and worker routes

### 6.1 Desktop ticket

```http
POST /api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/browser-view-ticket
Authorization: Bearer <user token>

201
{
  "ticket": "<opaque>",
  "expiresIn": 300,
  "protocolVersion": 1
}
```

The response is `Cache-Control: no-store`. Errors include:

- `BROWSER_SESSION_UNAVAILABLE`
- `BROWSER_POLICY_DENIED`
- `BROWSER_VIEWER_IN_USE`
- `INVALID_BROWSER_TICKET`

### 6.2 Desktop stream

```text
GET /api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/browser-view/stream?ticket=...
Upgrade: websocket
```

The ticket is the only query credential and is redacted from access logs.
After upgrade, the server sends `hello`, then `state`, then the first binary
frame. Compression is disabled because JPEG is already compressed.

### 6.3 Worker stream

```text
GET /worker/sessions/{sessionId}/browser-stream
Authorization: Worker <worker token>
Upgrade: websocket
```

The route validates organization, session, worker ID, epoch, and
`worker:transport`. A newer valid connection replaces the older one.

### 6.4 Loopback browserd stream

```text
GET /api/v1/browser/viewer-stream
X-AO-Browser-Capability: <session capability>
Upgrade: websocket
```

It binds only through browserd's existing `127.0.0.1` listener. The route is
not added to the desktop public daemon contract.

## 7. Stream protocol

### 7.1 Version and limits

- Protocol version: `1`.
- JSON control message limit: 32 KiB.
- Text insertion limit: 8 KiB per message.
- URL limit: 8 KiB.
- Binary frame limit: 1 MiB plus header.
- Maximum accepted input rate: 120 messages per second.
- Pointer move rate after client coalescing: 30 per second.
- Wheel rate after client accumulation: 60 per second.
- Ping interval: 20 seconds. Three missed responses retire the socket.

### 7.2 Ordered control messages

Control messages are JSON text. Every message includes `type`, `version`, and
`streamEpoch`. Input messages also carry a strictly increasing `inputSeq`.

Worker and browserd messages:

- `hello`
- `attach`
- `attached`
- `detach`
- `state`
- `viewport`
- `viewport_ack`
- `input`
- `input_ack`
- `input_rejected`
- `agent_action`
- `navigate`
- `tab`
- `dialog`
- `control_owner`
- `error`
- `ping`
- `pong`

The public viewer receives the allowlisted subset needed by the UI. Worker IDs,
capabilities, raw CDP event names, and internal error text are removed.

An accepted input acknowledgement includes `minFrameSeq`, which is one greater
than the newest frame sequence browserd had accepted before dispatch. An
`agent_action` start message carries the same marker for a mutating agent
operation. The renderer correlates a visible result with the first changed
frame at or above that marker. Actions that produce no visual change are
reported separately and do not create a false latency sample.

### 7.3 Binary frame envelope

Frames use one binary WebSocket message:

```text
4 bytes   magic "AOBR"
1 byte    protocol version
1 byte    frame kind, 1 = JPEG
8 bytes   stream epoch, unsigned big endian
8 bytes   frame sequence, unsigned big endian
2 bytes   width
2 bytes   height
8 bytes   capture monotonic milliseconds
1 byte    target ID length
N bytes   UTF-8 target ID
remaining JPEG bytes
```

The decoder rejects unknown versions, unknown codecs, zero dimensions,
non-increasing sequence within an epoch, target IDs over 128 bytes, truncated
headers, and payloads over the negotiated limit.

### 7.4 Input mapping

Allowed user inputs are:

- pointer move, down, up, and double-click;
- wheel delta;
- raw key down and key up with normalized modifiers;
- committed text insertion;
- composition start, update, commit, and cancel;
- navigation, reload, back, and forward;
- tab select, new, and close;
- JavaScript dialog accept and dismiss;
- viewport resize.

The renderer uses a focusable surface plus a hidden text input for committed
text and composition. App-level shortcuts stay app-level. Browser shortcuts
such as reload and focus location are translated to explicit viewer controls.
Clipboard, file paths, arbitrary JavaScript, and raw CDP commands are rejected.

## 8. Active target and tab synchronization

browserd is the authoritative active-target coordinator for the viewer. It
tracks target creation, destruction, metadata changes, and activation.

After every agent action that may change tabs or navigation, the Stage 1
service asks the engine for the current tab list and publishes one normalized
state update. Viewer tab actions call the same service-level operations used by
agent tab commands, then publish the same normalized state. This prevents the
viewer controller and the engine namespace from keeping different active-tab
state.

When the active target changes:

1. stop the old target's screencast;
2. detach its viewer CDP session;
3. attach to the new target;
4. apply the accepted viewport;
5. start screencast;
6. emit state before the first new-target frame.

Frames for the old target received after step 1 are discarded.

## 9. Renderer behavior

The existing Browser panel chrome remains shared between local and Cloud
sessions. `useBrowserView` selects a transport:

- local session: current Electron native browser view;
- Cloud Chromium session: new cloud stream client;
- proxy-only session: current static proxy preview.

The Cloud surface renders into a canvas. It provides:

- loading state before the first frame;
- reconnect state while retaining the latest painted frame;
- fatal state with request ID and retry action;
- `Agent controlling`, `You are controlling`, and idle indicators;
- an input focus ring that makes keyboard ownership visible;
- disabled input while viewport size is unacknowledged;
- current URL, loading state, navigation controls, and normalized tab state.

The renderer keeps exactly one decoded frame plus the currently painted frame.
It closes decoded bitmaps after paint. Hidden panels keep the socket attached
only while the session is actively selected or popped out. Merely hiding the
panel does not send fake input or viewport changes.

## 10. Lifecycle and recovery

### 10.1 Attach

1. Renderer mints a ticket and opens the public socket.
2. Relay registers the controlling viewer and sends `attach` to the worker.
3. Worker opens the browserd loopback viewer socket if needed.
4. browserd calls `EnsureRunning`, attaches to the active target, applies the
   viewport, and starts screencast.
5. `attached`, state, and a full frame travel back to the renderer.

### 10.2 Detach and idle shutdown

When the final viewer detaches, browserd stops screencast and releases user
control immediately. Chromium stays alive under the existing 30-minute idle
policy. An attached viewer prevents idle shutdown, but an idle worker socket
does not.

### 10.3 Viewer reconnect

The renderer mints a new ticket and reconnects with exponential delays of
250 ms, 500 ms, 1 s, 2 s, then 5 s. It keeps the last frame dimmed with a
`Reconnecting` label. A successful reconnect receives current state and a new
full frame. Input is never replayed after a disconnect.

### 10.4 Worker reconnect or replacement

The worker redials from 500 ms through 5 s. The relay fences the old epoch. If
a viewer is waiting, the new worker receives `attach` immediately. Sequence
counters reset under the new stream epoch, and the viewer clears any pending
input acknowledgements.

### 10.5 Chromium crash

browserd emits a sanitized `browser_restarting` state, invokes the existing
restart policy, reattaches after readiness, and emits a full frame. The profile
directory remains unchanged. Parking after repeated failures produces the
existing parked error and a visible retry action.

### 10.6 Control-plane restart

Both sockets reconnect. No media recovery is attempted. The current Chromium
page remains intact inside the VM, and the replacement relay requests a fresh
frame.

## 11. Security and privacy

1. Chromium debugging stays bound to `127.0.0.1` inside the VM.
2. browserd remains capability-authenticated and session-scoped.
3. The public viewer never receives a browserd capability, worker credential,
   CDP endpoint, provider address, or VM address.
4. Viewer authorization is checked at ticket issue and redemption.
5. Tickets are random, hashed at rest, one-use, epoch-bound, and short-lived.
6. Frame, input, URL, title, and dialog content are never logged or persisted.
7. Metrics contain sizes, timings, counts, states, and sanitized error codes
   only.
8. Access logs redact the ticket query parameter.
9. WebSocket origin validation uses the configured desktop and Cloud origins
   in addition to ticket validation.
10. Rate limits and message bounds are enforced independently on renderer,
    relay, worker, and browserd boundaries.
11. Page content remains untrusted. It cannot send control messages merely by
    drawing pixels or handling browser input.
12. Viewer navigation accepts only `http` and `https`. `localhost` resolves
    inside the session VM, preserving Stage 1 behavior.

## 12. Telemetry

Emit histograms and counters for:

- ticket issue and redemption outcome;
- viewer attach to first frame;
- Chromium cold start to first frame;
- frame capture, encode, relay, decode, and paint duration;
- frame bytes, dimensions, quality, and rate;
- frames captured, replaced, sent, rejected, and painted;
- input capture to browserd acknowledgement;
- input capture to first later frame;
- viewer and worker reconnects;
- control-owner duration and rejected input counts;
- browser restart and parked outcomes.

End-to-end latency uses a client-generated input sequence and monotonic
timestamps within each process. Input and action markers correlate protocol
segments without subtracting clocks from different machines. Wall-clock
timestamps are diagnostic only because VM and desktop clocks may differ.

No metric label may contain URL, title, typed text, target ID, session ID,
organization ID, or user ID.

## 13. Failure behavior

| Failure | User behavior | Agent behavior |
| --- | --- | --- |
| Ticket expired or used | Mint once more, then show sign-in or policy error | Unaffected |
| Worker stream absent | Viewer shows `Waiting for session` | Stage 1 verbs use loopback when worker is alive |
| Viewer slow | Old frames replaced, quality may step down | Unaffected |
| Input rate exceeded | Excess coalescible events dropped, discrete event gets visible rejection | Unaffected |
| User lease active | Viewer remains interactive | Mutating command waits up to 3 s, then receives stable error |
| Agent mutation active | Viewer paints frames but input is rejected with owner state | Command completes normally |
| Browser crash | Reconnecting state, then fresh frame or parked error | Existing restart and parked errors |
| Active tab closes | Next active tab is selected and full frame requested | Tab list reflects the same selection |
| Relay restarts | Both sockets reconnect, last frame remains dimmed | Browser and profile remain alive |

## 14. Implementation plan

### Step 1. browserd viewer controller

- Add loopback viewer route and protocol codec.
- Add CDP client attachment against `Chromium.EnsureRunning`.
- Implement screencast, viewport, input allowlist, target switching, and
  latest-frame buffer.
- Add the shared input arbiter around Stage 1 service execution.
- Keep the existing command and status routes byte-compatible.

Exit gate: a loopback integration test receives frames, sends click and text,
then confirms a Stage 1 snapshot sees the changed page.

### Step 2. worker and control-plane relay

- Add the worker outbound browser stream and browserd bridge.
- Add ticket persistence, issue, redemption, and public viewer routes.
- Add an in-memory session hub with one worker, one controller, bounded state
  queues, and latest-frame slots.
- Fence by worker epoch and disable compression.
- Add feature flags and fixed-replica deployment assertion.

Exit gate: an HTTP/WebSocket integration test relays a binary frame and input
in opposite directions without creating a worker request or database media row.

### Step 3. desktop viewer and input

- Add typed Cloud client ticket support.
- Add a renderer-side shared browser stream client.
- Render frames in BrowserPanel and connect navigation, tabs, focus, pointer,
  wheel, keyboard, text, composition, and resize.
- Add owner, loading, reconnect, and fatal UI states.

Exit gate: desktop component tests cover coordinate mapping, focus, owner
state, reconnect, and single-socket reuse across panel remounts.

### Step 4. shared-control parity and resilience

- Normalize agent and viewer active-tab updates.
- Finish user and agent arbitration behavior.
- Add dialogs, popups, crash recovery, adaptation, and telemetry.
- Verify no-browser-intent sessions still launch no Chromium process.

Exit gate: the full bidirectional scenario and every recovery scenario in
section 15 pass locally.

### Step 5. remote validation and rollout

- Validate through the normal desktop application on Docker.
- Validate a real remote Coder workspace used for test infrastructure.
- Run a staging canary on NodeOps before enabling live sessions.
- Capture actual-app screenshots and a short recording for review.
- Publish measured percentiles and resource use by provider.

Exit gate: all release criteria in section 16 pass, with rollback verified by
turning off the live viewer flag while retaining Stage 1 automation.

## 15. Test matrix

### 15.1 Unit tests

- Binary frame encode and decode, malformed headers, limit enforcement.
- Latest-frame replacement and independent ordered state queue.
- Ticket issue, expiry, one-use redemption, wrong user, wrong session, stale
  worker epoch.
- One-controller enforcement and same-principal reconnect replacement.
- Input allowlist, URL scheme validation, rate limits, coordinate mapping,
  modifier normalization, wheel accumulation, and composition limits.
- Control arbiter owner transitions, lease renewal, wait timeout, disconnect,
  and crash reset.
- Adaptive quality step-down and conservative step-up.
- Viewport debounce, accepted bounds, stale-frame input lockout.

### 15.2 browserd integration tests

Use a local static test page with a button, text input, scroll region, dialog,
popup, and visible action log.

1. Viewer receives the initial frame.
2. Stage 1 click changes the page and a changed viewer frame arrives.
3. Viewer click changes the page and Stage 1 snapshot sees the result.
4. Viewer text and composition update the input, and Stage 1 `get value`
   returns the committed value.
5. Agent tab creation and selection move the viewer to the same target.
6. Viewer tab selection changes the target used by the next agent command.
7. Resize changes both frame dimensions and the agent-visible layout.
8. A slow reader does not increase frame memory beyond the configured bound.
9. Chromium kill triggers restart and a replacement frame.
10. Two sessions do not share cookies, targets, frames, or input.

### 15.3 Control-plane integration tests

- Worker and viewer sockets pair only for the same organization, session, and
  current worker epoch.
- A frame reaches the viewer before any optional telemetry flush.
- Input reaches the worker without an `ao_worker_requests` row.
- A slow viewer replaces frames instead of blocking the worker reader.
- Disconnect tears down hub state and releases controller ownership.
- Ticket query values are absent from captured logs.
- Drain closes sockets with a retryable code.
- Feature disabled returns a stable unavailable response without affecting
  terminal streaming or Stage 1 browser commands.

### 15.4 Actual application end-to-end tests

Run these through the real desktop app and normal Cloud task/session flow:

1. Start a Cloud session without browser intent and verify Chromium is absent.
2. Open the Browser tab and verify cold start to first frame.
3. Ask the running session to open the fixture and click its button. Verify the
   click appears in the viewer.
4. Click a second button in the viewer. Verify the next agent snapshot includes
   the changed state.
5. Type into a field in the viewer. Verify the next agent query returns the
   exact value.
6. Let the agent type into the field. Verify each visible change arrives in the
   viewer without manual refresh.
7. Scroll from each side and verify the other side sees the resulting viewport.
8. Exercise navigation, tab open/select/close, popup, and JavaScript dialog.
9. Disconnect the desktop network, reconnect, and verify no click or key event
   is replayed.
10. Kill Chromium, then verify restart, retained profile, and a fresh frame.
11. Restart the control plane and verify both sockets recover.
12. Keep a viewer deliberately slow and verify frame drops, bounded memory,
    and responsive input after recovery.

Record timings for each run. Docker, Coder, and NodeOps reports stay separate.

### 15.5 Regression checks

- Full Stage 1 browser verb battery, including `localhost:3000`.
- Negative Stage 1 capability and unsupported-action paths.
- Browser idle shutdown and lazy restart.
- Cloud cold-start preparation with and without early browser intent.
- Terminal stream and relay suites.
- Workspace file, diff, and terminal transport suites.
- Cloud Go unit, race, vet, and build checks.
- Frontend typecheck, unit tests, and build.
- API and generated client drift checks when public DTOs are added.

## 16. Release criteria

Stage 2 is ready for a provider only when all of these are true:

1. One shared Chromium is proven by user action followed by an agent snapshot,
   and by agent action followed by a viewer frame.
2. The latency targets in section 1.2 pass on at least 100 warm-browser and 30
   cold-browser samples for that provider.
3. Peak relay memory remains bounded with a stalled viewer and a high-churn
   page.
4. No frame, typed text, URL, title, cookie, dialog text, or screenshot appears
   in PostgreSQL, logs, metrics labels, or durable event payloads.
5. A session with no browser intent starts no Chromium process.
6. Viewer failure does not break Stage 1 browser verbs.
7. Expired, reused, cross-session, cross-user, and stale-epoch tickets fail.
8. Browser debugging remains loopback-only in the VM.
9. Docker and Coder actual-app flows pass, then the NodeOps staging canary
   passes before live enablement.
10. Rollback disables the viewer without changing sessions, profiles, or Stage
    1 automation.

## 17. Deferred follow-ups

- WebRTC transport if CDP screencast cannot meet measured latency or bandwidth
  targets after adaptation.
- Audio.
- Clipboard and file transfer with explicit user consent and separate policy.
- Download surfacing in the Files panel.
- Network capture and DevTools.
- Multiple viewers, observers, and collaborative control.
- A dedicated browser relay service or session-hashed relay tier required
  before restoring multi-replica control-plane operation.
