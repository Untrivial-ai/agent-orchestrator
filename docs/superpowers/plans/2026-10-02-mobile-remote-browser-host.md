# Mobile Remote Browser Host — Implementation Plan

> **For implementation workers:** Execute this plan phase-by-phase. Keep each task in its own focused commit, run the narrow tests first, then the full repository validation listed at the end.

**Goal:** Let a paired AO Mobile client view and control the exact browser owned by an AO session while AO Desktop remains running, so the phone and the agent share tabs, cookies, profiles, navigation state, and page mutations.

**Architecture:** AO Desktop's Electron main process is the first `ao-browser-host`. It continues to own each session's hardened `WebContentsView` and Chromium profile. The daemon brokers a private, authenticated local control/media protocol to that host. Mobile connects only to the daemon through a dedicated authenticated WebSocket on the existing Connect Mobile listener. The daemon never renders pages, Electron never opens a network-facing listener, and mobile never receives the worker-only browser capability.

```text
AO Mobile
  | authenticated browser WebSocket (frames + bounded input)
  v
AO daemon
  | private local browser-stream socket (token-authenticated)
  v
ao-browser-host (Electron main-process subsystem)
  | Electron debugger / Chrome DevTools Protocol
  v
session Chromium WebContentsView
```

**Initial media transport:** CDP `Page.startScreencast` producing bounded JPEG frames, carried as binary WebSocket messages. WebRTC is deliberately deferred until device measurements show that the image stream cannot meet the product target. Control messages remain transport-independent so a later WebRTC video track does not change authorization, leasing, or input semantics.

**Tech stack:** Go (`chi`, `coder/websocket`), Electron/TypeScript (`WebContentsView`, `webContents.debugger`), Expo/React Native (`WebSocket`, `react-native-gesture-handler`, native image decoding surface).

---

## Product contract

The first release must support this workflow:

1. AO Desktop and Connect Mobile are running on the user's computer.
2. The user opens an AO session from the phone and taps **Live browser**.
3. The daemon asks Electron to create or reuse that session's existing browser.
4. Mobile receives the active tab, URL/title/loading state, and live frames.
5. Touch, scroll, typing, back/forward, reload, tab selection, tab creation, and tab close operate that same desktop-owned browser.
6. Agent browser actions and mobile input never execute concurrently. The phone temporarily becomes read-only while an agent browser command is active.
7. Opening the desktop Browser panel shows the same target and immediately revokes mobile control; the mobile stream may remain view-only.
8. Leaving the mobile browser screen stops capture. It does not destroy the browser, tabs, profile, or session.
9. Disabling Connect Mobile, rotating its password, terminating the AO session, or quitting AO Desktop closes the stream and releases control.

### Explicit non-goals

- Running a browser on the phone itself.
- Running after AO Desktop has quit or the host computer is asleep.
- Browser audio, camera, microphone, screen sharing, file pickers, downloads, clipboard reads, DevTools, drag-and-drop, or multi-touch page gestures from mobile.
- Multiple simultaneous mobile controllers. Extra viewers are allowed but read-only.
- Exposing the existing worker capability (`X-AO-Browser-Capability`) to a phone.
- Replacing the existing static session Preview WebView.
- WebRTC, STUN, TURN, or a public media relay in the first release.

---

## Load-bearing decisions

### 1. `ao-browser-host` is a role before it is a separate executable

The existing Electron process already owns AO's browser tabs, partitions, history, download manager, permission policy, annotations, agent-browser CDP bridge, and native desktop surface. Moving ownership to another Chromium process would force the desktop Browser panel to become a remote video client and would regress selection, accessibility, DevTools, downloads, and native rendering.

For this release, extract a clear `BrowserHost` interface and stream service inside Electron main. The daemon speaks only the private protocol and does not depend on that deployment choice. A future daemon-only feature can replace Electron with a standalone `ao-browser-host` implementation without changing the mobile protocol.

### 2. Media gets its own sockets

Do not put browser frames on `/mux`, which carries terminals and session events. Large image frames must not cause head-of-line blocking for terminal input, agent output, approvals, or notifications.

Use two dedicated links:

- `browser-stream.sock` / a named pipe between daemon and Electron.
- `GET /api/v1/sessions/{sessionId}/browser/live` between daemon and mobile.

Keep the existing newline-JSON browser command broker unchanged for worker browser actions. This isolates the new high-bandwidth path and reduces regression risk.

### 3. One capture per session, fan-out in the daemon

Electron produces one canonical stream per AO session at a maximum of 1280x720. The daemon fans the latest frame out to viewers. Do not start one CDP screencast per phone.

Every queue is latest-frame-wins with capacity one. A slow phone drops old frames; it must never slow Chromium, the agent, another phone, or the daemon.

### 4. The browser remains desktop-authoritative

The canonical browser viewport is 1280x720 while remotely viewed. Mobile letterboxes it and sends normalized coordinates. It does not resize the page on device rotation, because doing so would reflow the page under an agent or visible desktop user.

When the desktop panel becomes visible, its measured viewport becomes authoritative and a stream metadata event announces the new dimensions. Mobile remaps coordinates to the new frame.

### 5. Browser content requires an extra opt-in

Connect Mobile already authorizes powerful operations, but a logged-in browser can expose additional personal information. Add **Allow live browser on paired devices**, default off, to Connect Mobile settings. Persist it beside the other mobile bridge settings. The browser WebSocket must fail closed when it is off.

No new unauthenticated route is permitted. The existing identity probe remains the sole LAN authentication exemption.

---

## Protocol contracts

### Mobile WebSocket

Route:

```text
GET /api/v1/sessions/{sessionId}/browser/live
Authorization: Bearer <Connect Mobile password>
X-AO-Install-Id: <stable device installation id>
Sec-WebSocket-Protocol: ao.browser.v1
```

The handler must require the authenticated LAN-listener context, not merely a bearer-looking header. A request arriving on the unauthenticated loopback listener returns `404`, preserving the listener boundary. The install ID identifies the controller lease; it is not an authentication secret.

Text control messages use a versioned discriminated JSON envelope. Client messages:

```ts
type BrowserClientMessage =
  | { type: "start"; role: "controller" | "viewer" }
  | { type: "heartbeat"; leaseId?: string }
  | { type: "pointer"; leaseId: string; phase: "move" | "down" | "up"; x: number; y: number; button?: "left" | "middle" | "right" }
  | { type: "wheel"; leaseId: string; deltaX: number; deltaY: number }
  | { type: "key"; leaseId: string; phase: "down" | "up"; key: string; code: string; modifiers?: string[] }
  | { type: "text"; leaseId: string; value: string }
  | { type: "navigate"; leaseId: string; action: "open" | "back" | "forward" | "reload" | "stop"; url?: string }
  | { type: "tab"; leaseId: string; action: "new" | "select" | "close"; tabId?: string; url?: string };
```

Server messages:

```ts
type BrowserServerMessage =
  | { type: "ready"; sessionId: string; role: "controller" | "viewer"; leaseId?: string; width: number; height: number }
  | { type: "state"; url: string; title: string; loading: boolean; canGoBack: boolean; canGoForward: boolean; activeTabId: string; tabs: BrowserTab[] }
  | { type: "control"; state: "granted" | "view-only" | "agent-active" | "desktop-active" | "expired" }
  | { type: "quality"; fps: number; quality: number; droppedFrames: number }
  | { type: "error"; code: string; message: string; recoverable: boolean };
```

Binary frame layout, network byte order:

```text
0..3    magic "AOBR"
4       protocol version (1)
5       codec (1 = JPEG)
6..7    flags
8..15   sequence uint64
16..23  capturedAtUnixMs uint64
24..25  width uint16
26..27  height uint16
28..N   encoded image bytes
```

Reject text messages over 64 KiB, binary client messages, invalid finite coordinates, text input over 16 KiB, URLs outside HTTP(S), unknown modifiers/keys, and pointer/wheel rates above the documented limit. Close malformed or policy-violating clients with stable WebSocket close codes.

### Private daemon-to-host protocol

Create a separate protocol version `1` rather than changing `browserruntime.ProtocolVersion`. The existing browser command transport remains version `2`.

The daemon listens on a Unix socket under the existing short runtime alias on macOS/Linux and a named pipe on Windows. The address is added to `running.json`; the existing per-launch browser runtime token authenticates the host hello. Nothing is TCP-bound.

Frames are length-prefixed and bounded:

```text
uint32 totalLength
uint8  kind          // 1 JSON control, 2 JPEG frame
bytes  body
```

JSON commands include `start`, `stop`, `input`, `navigate`, `tab`, and `lease-state`. A binary frame body includes a daemon-assigned uint32 stream ID followed by the same sequence/timestamp/dimensions metadata and JPEG payload used on mobile.

Limits:

- Maximum encoded frame: 2 MiB.
- Maximum control frame: 64 KiB.
- One active capture per session.
- Maximum four simultaneously captured sessions initially.
- Latest-frame queue capacity: one per session and one per mobile viewer.
- Host heartbeat every 10 seconds; unavailable after 30 seconds.

---

## Phase 1 — Decision record and feature gate

### Task 1: Record the architecture and mobile security boundary

**Files:**

- Create: `docs/adr/0005-mobile-remote-browser-host.md`
- Modify: `docs/architecture.md`
- Modify: `docs/README.md`

- [ ] Document why the browser stays Electron-owned for the first release.
- [ ] Document the dedicated WebSocket and private socket, one-capture fan-out, latest-frame backpressure, controller lease, and WebRTC deferral.
- [ ] Record that browser frames over LAN inherit ADR 0001's plaintext limitation and Cloudflare traffic inherits ADR 0004's TLS-termination limitation. Tailscale remains the preferred remote path for sensitive browser content.
- [ ] State that a future standalone host is a compatible implementation of the private protocol, not a new mobile contract.

### Task 2: Add the persisted opt-in

**Files:**

- Modify: `backend/internal/mobilebridge/config.go`
- Modify: `backend/internal/mobilebridge/config_test.go`
- Modify: `backend/internal/httpd/controllers/mobile.go`
- Modify: `backend/internal/httpd/controllers/mobile_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`
- Modify: `frontend/src/renderer/components/settings/ConnectMobileContent.tsx`
- Modify: relevant Connect Mobile component tests
- Regenerate: `backend/internal/httpd/apispec/openapi.yaml`
- Regenerate: `frontend/src/api/schema.ts`

- [ ] Add `BrowserRemoteControl bool` to `mobilebridge.State`, default false for existing installations.
- [ ] Add `browserRemoteControl` to `MobileStatusResponse` and a loopback-only `POST /api/v1/mobile/browser-control` request `{enabled:boolean}`.
- [ ] Reuse the existing mobile-control transport block so a phone cannot enable its own browser access.
- [ ] Turning the setting off synchronously closes all live browser WebSockets and tells the host to stop every capture before returning success.
- [ ] Add the desktop toggle and explicit LAN/Cloudflare privacy copy.
- [ ] Keep `MobileAPIVersion` at 1 because this is additive; older phones ignore the new field and route.
- [ ] Run `npm run api` and the focused mobile controller/frontend tests.

---

## Phase 2 — Private browser-stream link

### Task 3: Implement the daemon-side framed broker

**Files:**

- Create: `backend/internal/browserstream/protocol.go`
- Create: `backend/internal/browserstream/broker.go`
- Create: `backend/internal/browserstream/listen_unix.go`
- Create: `backend/internal/browserstream/listen_windows.go`
- Create: `backend/internal/browserstream/broker_test.go`
- Modify: `backend/internal/daemon/daemon.go`
- Modify: run-file DTO/writer tests that expose runtime addresses

The package owns host connection state and session subscriptions but has no HTTP dependency.

- [ ] Add token-authenticated hello with protocol version and a five-second deadline.
- [ ] Allow one current host connection. A newly authenticated host replaces the old connection and fails/restarts affected streams deterministically.
- [ ] Implement `Start(ctx, sessionID)`, `Stop(sessionID)`, `SendInput(...)`, `Subscribe(sessionID)`, `Status()`, and `Close()`.
- [ ] Reference-count session subscribers. Send host `start` only on 0→1 and `stop` only on 1→0.
- [ ] Decode/validate bounded frames before allocation and reject unknown stream IDs.
- [ ] Store only the newest frame per session. Never hold the broker lock during socket writes or subscriber callbacks.
- [ ] Publish `browserStreamAddress` through `running.json`, never the token.
- [ ] Reuse the private stdin token handoff already used by the command broker.
- [ ] Cover authentication failure, oversized frames, partial reads, replacement, reconnect, subscriber cancellation, slow subscriber frame dropping, and session isolation.

### Task 4: Implement the Electron stream link

**Files:**

- Create: `frontend/src/main/browser-stream-link.ts`
- Create: `frontend/src/main/browser-stream-link.test.ts`
- Modify: `frontend/src/main.ts`
- Modify: run-file parsing/types tests

- [ ] Connect to `browserStreamAddress` only after the daemon is ready and the `BrowserHost` exists.
- [ ] Authenticate with the existing per-launch token and reconnect with bounded exponential backoff.
- [ ] Parse framed commands incrementally across arbitrary socket chunk boundaries.
- [ ] Expose callbacks for start, stop, input, navigation, tab operations, and lease state.
- [ ] Serialize commands per AO session while allowing different sessions to proceed concurrently.
- [ ] Bound outbound media buffering to one frame per session; replace queued frames rather than appending.
- [ ] On disconnect, cancel captures and remote-input leases without destroying browser sessions.
- [ ] Keep the command link and stream link lifecycle independent so a media failure cannot break agent browser commands.

---

## Phase 3 — Electron `ao-browser-host`

### Task 5: Extract a stream-capable host contract

**Files:**

- Modify: `frontend/src/main/browser-view-host.ts`
- Modify: `frontend/src/main/browser-view-host.test.ts`
- Create: `frontend/src/main/browser-live-types.ts`

Extend `BrowserViewHost` with:

```ts
startLiveStream(sessionId: string, sink: BrowserFrameSink): Promise<BrowserLiveState>
stopLiveStream(sessionId: string): Promise<void>
handleRemoteInput(sessionId: string, input: BrowserRemoteInput): Promise<void>
handleRemoteNavigation(sessionId: string, input: BrowserRemoteNavigation): Promise<void>
handleRemoteTab(sessionId: string, input: BrowserRemoteTabAction): Promise<void>
setRemoteLeaseState(sessionId: string, state: BrowserRemoteLeaseState): void
```

- [ ] Reuse `ensureSessionReady`; do not construct a second browser or profile.
- [ ] Track live viewers separately from renderer owners and agent commands.
- [ ] Emit sanitized navigation/tab state on changes; never emit cookies, storage, request headers, page source, console payloads, filesystem paths, or profile paths.
- [ ] Preserve the browser when the last live viewer leaves.
- [ ] Destroy capture state during existing session/browser teardown.

### Task 6: Capture the active tab through CDP

**Files:**

- Create: `frontend/src/main/browser-screencast.ts`
- Create: `frontend/src/main/browser-screencast.test.ts`
- Modify: `frontend/src/main/browser-view-host.ts`
- Modify: `frontend/src/main/agent-browser-cdp-bridge.ts` only if shared debugger event routing needs a reusable helper

- [ ] Use the existing shared Electron debugger attachment; do not attach a second debugger or expose raw CDP to mobile.
- [ ] Start `Page.startScreencast` with JPEG, quality 70, maximum 1280x720, `everyNthFrame: 1`.
- [ ] Accept only `Page.screencastFrame` events belonging to the currently active tab.
- [ ] Copy/validate frame metadata, enqueue the latest frame, then promptly call `Page.screencastFrameAck`. Do not wait for mobile delivery before acknowledging Chromium.
- [ ] Stop the previous tab's screencast and start the new active tab atomically on tab switch.
- [ ] Stop capture on navigation target destruction, profile switch, session destruction, host-link loss, or final subscriber removal.
- [ ] If CDP reports screencast invisibility or no frame arrives for two seconds, attempt one bounded restart and then surface `BROWSER_CAPTURE_UNAVAILABLE`; do not spin.
- [ ] Add `backgroundThrottling: false` to AO browser WebContents and maintain a non-zero 1280x720 offscreen capture bound while the desktop panel is hidden and a remote viewer exists.
- [ ] Keep UI visibility distinct from capture paintability so the native view never overlays unrelated desktop screens.
- [ ] Test duplicate start/stop, active-tab changes, ack behavior, stale events, missing debugger, oversized image data, and restart timeout.

### Task 7: Add safe remote input

**Files:**

- Create: `frontend/src/main/browser-remote-input.ts`
- Create: `frontend/src/main/browser-remote-input.test.ts`
- Modify: `frontend/src/main/browser-view-host.ts`

- [ ] Convert finite normalized coordinates `[0,1]` into the latest captured viewport.
- [ ] Inject mouse move/down/up and wheel events with `webContents.sendInputEvent`.
- [ ] Use CDP `Input.insertText` for Unicode/IME text rather than synthesizing individual key codes.
- [ ] Restrict keyboard modifiers and block AO/app/system shortcuts, including quit, close window/tab outside the explicit tab command, DevTools, reload bypass, and OS-level combinations.
- [ ] Coalesce pointer moves to at most 60/s and wheel events to at most 30/s before they enter the per-session queue.
- [ ] Reject input unless the matching lease is in `controller` state.
- [ ] Suspend mobile control while `agentBrowserCommands > 0`, during profile switching, or while the desktop browser panel has native focus.
- [ ] Desktop focus revokes the controller lease through the daemon; mobile must not silently regain it without a fresh request.
- [ ] Keep permission requests, file chooser, downloads, clipboard reads, and native dialogs unavailable from remote input.

### Task 8: Prove hidden/background capture in the real app

**Files:**

- Create: `frontend/e2e/browser-live-capture.spec.ts`
- Add a deterministic animated HTML fixture under the existing e2e fixture convention

- [ ] Verify frames advance with the Browser panel visible.
- [ ] Verify frames advance with the panel hidden and another AO surface visible.
- [ ] Verify navigation, tab switching, scroll, click, and text entry mutate the same WebContents later shown in the desktop Browser panel.
- [ ] Verify the stream stops without destroying tabs.
- [ ] Run on macOS locally; leave Windows/Linux capture validation to their CI runners if unavailable.
- [ ] Treat a minimized-window capture failure as a release blocker for the documented mobile-only workflow. If Electron cannot paint reliably, implement a dedicated hidden capture `BaseWindow` and reparent only the active session view while it has no visible desktop owner; do not silently reduce the requirement.

---

## Phase 4 — Daemon mobile gateway

### Task 9: Add authenticated-listener provenance

**Files:**

- Modify: `backend/internal/httpd/auth.go`
- Modify: `backend/internal/httpd/auth_test.go`
- Modify: `backend/internal/httpd/lan_listener_test.go`

- [ ] After successful LAN authentication, place an unforgeable internal marker in the request context containing source address and validated install ID.
- [ ] Do not trust a request header as listener provenance.
- [ ] Validate `X-AO-Install-Id` length/format; generate no identity server-side for an incoming socket.
- [ ] Confirm the loopback listener never has this context marker, even if the caller supplies the same headers.
- [ ] Preserve the exact unauthenticated identity-probe exemption and lockout behavior.

### Task 10: Implement browser stream authorization and leases

**Files:**

- Create: `backend/internal/browserlive/service.go`
- Create: `backend/internal/browserlive/service_test.go`
- Modify: daemon dependency wiring

The service owns policy; the WebSocket handler owns only transport adaptation.

- [ ] Validate that the session exists and is not terminated.
- [ ] Require Connect Mobile enabled, browser remote control enabled, authenticated LAN provenance, and a non-empty install ID.
- [ ] Permit many viewers but exactly one controller lease per session.
- [ ] Issue a random opaque lease ID, never the connection password or worker browser capability.
- [ ] Require a heartbeat every 10 seconds and expire after 30 seconds.
- [ ] Revoke on socket close, desktop takeover, agent activity, session termination, mobile disable, password rotation, browser-control opt-out, or host disconnect.
- [ ] Make revocation idempotent and always notify both host and clients.
- [ ] Return stable errors: `BROWSER_REMOTE_DISABLED`, `BROWSER_HOST_UNAVAILABLE`, `SESSION_TERMINATED`, `CONTROLLER_BUSY`, and `DEVICE_ID_REQUIRED`.
- [ ] Subscribe to the existing durable/session lifecycle signals rather than polling session status.

### Task 11: Add the dedicated WebSocket endpoint

**Files:**

- Create: `backend/internal/httpd/browser_live.go`
- Create: `backend/internal/httpd/browser_live_test.go`
- Modify: `backend/internal/httpd/router.go`
- Modify: `backend/internal/httpd/server.go`

- [ ] Mount `GET /api/v1/sessions/{sessionId}/browser/live` outside the general request timeout group.
- [ ] Require the `ao.browser.v1` subprotocol and authenticated LAN provenance before upgrading.
- [ ] Return `404` on the loopback listener and when the browser feature is not wired.
- [ ] Use one read goroutine and one write goroutine per connection. Only the writer touches the WebSocket write path.
- [ ] Send JSON as text messages and browser frames as binary messages.
- [ ] Give every viewer a one-slot latest-frame channel and count drops.
- [ ] Bound all reads, write deadlines, heartbeat deadlines, and close handshakes.
- [ ] Close cleanly with stable reasons on feature disable, credential rotation, session termination, or host loss.
- [ ] Cover LAN-vs-loopback routing, bad auth, missing device ID, unsupported subprotocol, viewer/controller behavior, malformed inputs, slow clients, disconnect cleanup, and frame fan-out.
- [ ] Do not add the WebSocket to OpenAPI; document it beside `/mux` as a non-HTTP-stream contract.

---

## Phase 5 — AO Mobile

### Task 12: Build the browser live protocol client

**Files:**

- Create: `packages/mobile/lib/browserLive/protocol.ts`
- Create: `packages/mobile/lib/browserLive/client.ts`
- Create: `packages/mobile/lib/browserLive/client.test.ts`
- Modify: `packages/mobile/lib/config.ts`

- [ ] Add `browserLiveUrl(config, sessionId)` using the currently selected LAN/Tailscale/tunnel endpoint.
- [ ] Construct React Native's WebSocket with Authorization, `X-AO-Install-Id`, Origin, and `ao.browser.v1` subprotocol.
- [ ] Set `binaryType = "arraybuffer"` and validate the 28-byte frame header before accepting payload bytes.
- [ ] Keep only the newest decoded/undecoded frame; release prior native image resources immediately.
- [ ] Reconnect with bounded backoff only while the screen remains foregrounded.
- [ ] Stop and release the lease when the app backgrounds, the route blurs, configuration changes, or the component unmounts.
- [ ] Do not silently downgrade a requested controller to viewer; display the server's control state.

### Task 13: Implement and benchmark the native frame surface

**Files:**

- Create: `packages/mobile/lib/browserLive/BrowserFrameSurface.tsx`
- Create: `packages/mobile/lib/browserLive/frameMetrics.ts`
- Modify: `packages/mobile/package.json`
- Modify: native Expo configuration only if required by the selected decoder

- [ ] Start with a native encoded-image decoder/rendering surface capable of consuming JPEG `ArrayBuffer` data without converting every frame to a JavaScript base64 URI. Prefer an Expo-compatible Skia surface if the platform image component cannot meet the target.
- [ ] Benchmark on one physical iPhone and one physical Android device at 1280x720, quality 70, for five minutes.
- [ ] Acceptance targets: median decode+present under 50 ms, p95 under 100 ms, no unbounded memory growth, no more than 5% UI-thread long frames, and stable 8 fps on a changing page.
- [ ] If the target fails, lower quality/resolution adaptively before introducing WebRTC. Record measurements in `docs/performance/mobile-browser-stream/`.
- [ ] Do not use a WebView data-URL loop or store frames on disk.

### Task 14: Add the mobile live-browser screen

**Files:**

- Create: `packages/mobile/app/browser/[id].tsx`
- Create: `packages/mobile/lib/browserLive/BrowserScreen.tsx`
- Create: focused component/source tests following current mobile conventions
- Modify: `packages/mobile/app/_layout.tsx`
- Modify: `packages/mobile/lib/chat/ChatSessionScreen.tsx`
- Modify: `packages/mobile/lib/session/TerminalSessionScreen.tsx`

- [ ] Add a distinct **Live browser** action; keep **Preview** for generated/static previews.
- [ ] Render the stream aspect-fit with explicit letterboxing and map gestures only inside the actual image rect.
- [ ] Support tap/click, long-press/right-click where the platform convention is clear, drag, two-finger scroll, and a hidden/native text input for keyboard entry.
- [ ] Add URL, back, forward, reload/stop, and tab controls using server state rather than optimistic local state.
- [ ] Show reconnecting, host unavailable, remote browser disabled, viewer-only, agent-active, desktop-active, and session-terminated states distinctly.
- [ ] Show a persistent control indicator so users know whether touches mutate the browser.
- [ ] Respect safe areas, rotation, Dynamic Type, VoiceOver/TalkBack labels, reduced motion, and app foreground/background transitions.
- [ ] Never log URLs, typed text, frame bytes, connection passwords, or lease IDs to analytics/crash breadcrumbs.

---

## Phase 6 — Desktop coordination and observability

### Task 15: Surface remote viewing/control on desktop

**Files:**

- Modify: shared browser IPC types/preload exposure
- Modify: `frontend/src/renderer/components/BrowserPanel.tsx`
- Modify: `frontend/src/renderer/components/BrowserPanel.test.tsx`

- [ ] Show **Viewed on mobile** and **Controlled on mobile** states in the relevant session Browser panel.
- [ ] Focusing or interacting with the desktop page sends a takeover event before injecting desktop input.
- [ ] Provide an explicit **Stop mobile access** action that revokes viewers for this session without disabling Connect Mobile globally.
- [ ] Do not reveal device install IDs; use the registered display name when available, otherwise “paired device”.

### Task 16: Add bounded operational telemetry

**Files:**

- Modify existing telemetry taxonomy/runbook files required by repository policy
- Add unit tests for event shape and redaction

- [ ] Measure stream opened/closed, transport kind, endpoint kind, negotiated dimensions/quality, frames sent/dropped, reconnect count, and categorized failure reason.
- [ ] Never include session IDs, URLs, titles, page pixels, typed input, device IDs, IP addresses, profile names, or lease tokens.
- [ ] Add local debug counters visible in diagnostics without sending browser content.

---

## Phase 7 — Hardening, validation, and rollout

### Task 17: Resource and adversarial tests

- [ ] Four captured sessions reject a fifth with a stable recoverable error.
- [ ] Ten viewers cannot grow memory without bound.
- [ ] A viewer that never reads frames does not affect other viewers or Chromium.
- [ ] Repeated connect/disconnect leaves no CDP screencast, debugger listener, timer, goroutine, WebSocket, or native image resource behind.
- [ ] Malformed lengths, oversized JPEGs, unknown stream IDs, stale sequence numbers, NaN/Infinity coordinates, event floods, and expired leases fail closed.
- [ ] Session A can never receive frames/state or inject input into session B.
- [ ] Password rotation and Connect Mobile disable close existing upgraded sockets, not only future requests.
- [ ] Browser profile switching waits for capture/input to quiesce and cannot leak a frame from the old profile after completion.
- [ ] Agent browser screenshots, snapshots, network capture, annotations, and DevTools continue to work with live streaming active.

### Task 18: Adaptive quality policy

- [ ] Default to 1280x720, JPEG quality 70, target 8 fps.
- [ ] Degrade in this order when the viewer drops more than 20% of frames over five seconds: quality 70→55, fps 8→5, resolution 1280x720→960x540.
- [ ] Recover one step only after 20 stable seconds to avoid oscillation.
- [ ] Stop capture immediately when all viewers background/disconnect.
- [ ] Keep navigation/state messages reliable even when media frames are dropped.

### Task 19: Documentation and compatibility

**Files:**

- Modify: `packages/mobile/README.md`
- Modify: desktop remote-access user docs under `frontend/src/docs/content/`
- Modify: `docs/STATUS.md`
- Modify: `docs/architecture.md`

- [ ] Explain that AO Desktop must be running and the host computer must be awake.
- [ ] Explain the difference between Preview and Live browser.
- [ ] Explain LAN plaintext and Cloudflare termination risks for browser content; recommend Tailscale for sensitive remote use.
- [ ] Document unsupported controls and how desktop takeover works.
- [ ] Older mobile clients continue working; newer clients hide Live browser when the route returns 404 or the status field is absent.

### Task 20: Full verification

Run focused suites throughout, then before handoff run:

```bash
npm run lint
npm run frontend:typecheck
cd frontend && npm test
cd ../packages/mobile && npm run typecheck && npm test
cd ../../backend && go test -race ./...
cd .. && npx @redwoodjs/agent-ci run --all
```

Also perform manual real-device validation for:

- LAN Wi-Fi on iOS and Android.
- Tailscale on iOS and Android.
- Cloudflare tunnel on cellular, explicitly checking WebSocket frame flow.
- Desktop Browser panel hidden, visible, and taking control back.
- Agent automation while mobile watches.
- Connection loss, phone backgrounding, AO restart, password rotation, and session termination.

Do not claim a platform passed when its physical device/runner was unavailable; record the exact CI or manual-validation gap.

---

## Release gates

The feature remains behind the persisted desktop opt-in until all are true:

- No cross-session frame or input leak in adversarial tests.
- Hidden-panel capture works in the packaged desktop app on macOS, Windows, and Linux.
- Five-minute physical-device runs meet the decode, memory, and responsiveness targets.
- Slow viewers demonstrably cannot delay terminal or agent traffic.
- Password rotation and feature disable tear down already-open streams.
- Desktop takeover is deterministic and visibly reflected on mobile.
- Security/privacy copy is present before the first frame can be shown.

## WebRTC promotion criteria

Create a separate ADR and implementation plan for WebRTC only if, after adaptive JPEG streaming is implemented, physical-device data shows one or more of:

- p95 glass-to-glass latency remains above 300 ms on LAN/Tailscale.
- Stable interactive pages cannot sustain 8 fps within the CPU target.
- The product requires browser audio or video playback.
- Median browser-stream bandwidth materially harms other AO traffic.

That follow-up may replace only the media channel. Authorization, controller leases, session routing, input/state messages, lifecycle, and the `ao-browser-host` boundary remain unchanged.
