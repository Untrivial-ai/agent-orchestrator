# Spec: cloud (sandbox) browser, real Chromium per session

Status: proposed (brainstormed 2026-09-17, supersedes the staging in
`cloud-browser-handoff.md` from session 19; that document remains the context
dump and evidence record).

Sources read for this spec:

- `cloud-browser-handoff.md` (session 19 worktree, untracked there)
- Code verified in this checkout: `backend/internal/cli/browser.go`,
  `backend/internal/service/browser/{service.go,authority.go}`,
  `backend/internal/browserruntime/broker.go`,
  `cloud/internal/httpapi/{server.go,browser_handlers.go,browser_rewrite.go}`,
  `cloud/internal/workertransport/{supervisor.go,browser.go}`,
  `cloud/internal/worker/protocol.go`
- POC evidence: `docs/createos-live-browser.md` and assets (session 19
  worktree, untracked there)

---

## 1. Goal and non-goals

**Goal.** A cloud session's browser becomes one real Chromium inside the
sandbox VM, shared by the human (Cloud web app, Browser tab) and the agent
(`ao browser` verbs). Same verb set, same JSON result shapes, same UI
component as desktop. Per-session isolation. Survives pause/resume and
control-plane reconnects.

**Non-goals for v1.**

- Full DevTools UI in the cloud viewer (the `devtools-open` verb is a no-op
  with a clear status in cloud).
- WebRTC pixel transport (screencast over the existing channel first).
- Profile migration across VM replacement (profile is lost, documented).
- Removing the fetch-and-rewrite proxy (it stays as a flag-gated fallback).
- OS-native interstitials (print preview, OS file pickers).

## 2. Decisions

Each decision number is referenced by the sections below.

**D1. Agent automation executes entirely inside the VM.** The in-VM `ao`
CLI talks to a loopback browser service in the VM, which drives the VM
Chromium through the pinned agent-browser engine. No control-plane hop for
any `ao browser` verb. Consequences: 5 MiB screenshots never touch the 1 MiB
durable-queue bound; automation works during control-plane outages; latency
is loopback-local. This replaces the handoff's "browser.command over the
worker transport" for the agent path; the transport is used only by the
control plane (D5).

**D2. Reuse the backend browser service code inside the VM.**
`backend/internal/service/browser` (action allowlist, session/lifecycle
validation, result shaping) is transport-agnostic behind its port
interface. Compile it into the in-VM browser service with a new adapter
(agent-browser engine in the VM) instead of the Electron bridge. Wire
contract with the CLI is therefore the existing
`POST /api/v1/browser/commands` / `GET /api/v1/browser/status` pair, and the
CLI hand-mirrored DTOs stay valid unchanged.

**D3. Headless Chromium, CDP screencast for the human view.** No Xvfb, no
x11vnc, no websockify, no VNC, no ingress port. The supervisor attaches its
own CDP session and runs `Page.startScreencast` on the active tab; frames
leave the VM over the existing authenticated worker channel (D4); the
viewer connects only to the control plane over a WebSocket. Input flows back
as CDP `Input.dispatch*` on the same session. This matches the local UX (the
BrowserPanel is a viewport, not browser chrome) and removes the VNC password
problem entirely rather than fixing it.

**D4. Frames ride the existing worker channel; no new listeners, ever.**
The worker transport gains an internal endpoint the VM worker POSTs to with
its existing credentials: frame posts, tab events, dialog events, download
events. Poll/claim behavior of the durable queue is untouched for commands.
The control plane keeps only the latest frame per session in memory and
never persists frames. Hard cap 1 MiB per frame POST (typical JPEG viewport
frame is far smaller), quality and scale tunable.

**D5. Control-plane-initiated operations ride the durable queue as a new
`browser.control` kind.** Used by the viewer path only: tab select/new/close
on behalf of the UI, status queries, viewer attach/detach notification.
Bounded request/response, same 1 MiB result discipline as everything else
on the queue (tab metadata and status are small; screenshots do not travel
this path, see D1).

**D6. Auth: terminal-ticket pattern for the viewer, existing HMAC
capability in the VM.** The Cloud web app requests a short-lived (5 min),
single-purpose signed ticket bound to `sessionID` and `browser-view`,
presents it when opening the viewer WebSocket; the control plane validates
and expires. The in-VM CLI to browser-service hop keeps the existing
per-session HMAC capability scheme from `authority.go` verbatim.

**D7. Supervision: a separate supervised unit, lazy start.** A `browserd`
unit in the `workerexec` supervision tree, sibling to the agent process,
not embedded in the existing Supervisor. Starts on first browser verb or
viewer attach. Idle shutdown after 30 min with no attached viewer and no
recent agent verb. Restart with backoff; after 5 failed restarts in 10 min
it parks and the next `browser open` retries. Chromium flags: headless new
mode, `--remote-debugging-address=127.0.0.1` with a loopback port, per
session `--user-data-dir`, `--no-first-run`, no first-run network traffic.

**D8. Profile and tabs live on sandbox disk under the AO root analog.**
`/home/ao/.ao/browser/<sessionID>/{profile,tabs.json,downloads}` inside the
VM. Pause/resume: process dies, disk persists, tabs restored from
`tabs.json` on relaunch via CDP `Target.createTarget`. VM replacement loses
the profile; accepted and documented for v1.

**D9. The proxy stays, flag-gated.** Session attribute
`browser_engine: "chromium" | "proxy"` (default `chromium` when the sandbox
shape and image allow, else `proxy`). The existing rewrite pipeline in
`browser_handlers.go` / `browser_rewrite.go` is untouched and remains the
fallback. Deprecation is a separate future decision.

**D10. Concurrent CDP clients are allowed; single-writer per input source.**
Standalone Chromium accepts multiple simultaneous CDP WebSocket sessions
(each gets its own domain state). The automation engine and the
screencast supervisor attach independently. Viewer input and agent input
may interleave; that is the same semantics as two humans on one machine.
No cross-tab input: the supervisor binds its input session to the active
tab target only.

## 3. Component architecture

```text
Cloud web app (browser)
  SessionInspectorView browser tab  (reused from packages/product-ui)
    |  WSS + viewer ticket (D6)
    v
Control plane (cloud/internal)
  browser viewer hub        latest-frame cache per session, fan-out to viewers
  ticket issuer             short-lived, session-bound
  internal frame endpoint   VM-worker-authenticated POSTs (D4)
  browser.control queue     durable request/response for viewer-initiated ops (D5)
    ^  existing worker auth channel
    |
Sandbox VM
  worker (cloud/internal/workertransport)
    claims browser.control requests; forwards to browserd loopback
    POSTs frames/events to the control plane
  browserd (new, D7)
    supervises Chromium + agent-browser engine
    loopback HTTP: /api/v1/browser/commands, /api/v1/browser/status (D2)
    CDP sessions: automation (engine), screencast+input (own)
    profile/tabs/downloads under /home/ao/.ao/browser/<sessionID>/ (D8)
  in-VM `ao` CLI (unchanged) -> loopback browserd
```

The agent path never leaves the VM box. The viewer path is
viewer → control plane → queue → browserd → CDP → frame POST back up.

## 4. Flows

### 4.1 Agent verb (e.g. `ao browser act`)

1. In-VM CLI resolves the session, sends the command with the capability
   header to loopback browserd (identical to the local daemon contract).
2. browserd validates via the reused service (allowlist, lifecycle), runs
   the verb through the agent-browser engine against its CDP attachment.
3. Result JSON returns over loopback. Screenshots (≤ 5 MiB) stay in the VM.
   Untrusted-content markers in CLI output are unchanged.

Error surface: same envelopes and codes as local, plus
`BROWSER_ENGINE_PROXY` (verb unavailable because the session runs the
proxy engine) and `BROWSER_ENGINE_STOPPED` (parked after restart failures).

### 4.2 Human view

Attach: UI requests ticket → opens WSS → control plane sends
`browser.attach` via the queue → browserd (lazy-)starts Chromium if needed,
begins screencast on the active tab, POSTs frames (D4) → hub caches latest,
pushes to viewers. Frame format: JPEG, panel-width scaled, quality ~70,
on-change plus max 10 fps; ack-based backpressure (next frame only after
viewer ack; unacked frame is replaced, never queued).

Input: viewer WS message `{type:"input", tab, input}` → control plane wraps
as `browser.control` → browserd dispatches `Input.dispatchMouseEvent` /
``Input.dispatchKeyEvent`` / `Input.insertText`` on the active tab. Dialogs:
`Page.javascriptDialogOpening` events surface to the UI as a dialog prompt
(reuses the local dialog UI shape); accept/dismiss rides the same control
path.

Tabs: CDP target events → tab metadata events (id, title, url, favicon,
active) POSTed as events → tabs rail renders; tab select/new/close from
the UI ride `browser.control`. The unseen-tab badge is driven by tab
events while no viewer is attached, same semantics as local.

### 4.3 Lifecycle

- Session start: nothing browser-related launches (lazy, D7).
- First browser verb or viewer attach: browserd starts, restores tabs from
  `tabs.json` if present.
- Pause: processes die with the VM; disk state (D8) persists.
- Resume: next verb or attach relaunches and restores tabs.
- Control-plane outage: agent verbs unaffected (D1). Viewer drops, reattach
  with a fresh ticket when the plane returns; missed frames are fine
  (latest-keyframe semantics, D4).
- Session end: profile directory deleted with the sandbox.

## 5. Wire contracts

**Unchanged:** loopback `/api/v1/browser/{commands,status}` request and
response DTOs, capability header, action allowlist (D2).

**New worker protocol kinds** (`cloud/internal/worker/protocol.go`):

- `BrowserControlRequest{Kind, Sequence, Op, TabID, Payload}` and
  `BrowserControlResponse{OK, Error, Payload}` on the durable queue.
- `BrowserEventFrame{SessionID, Seq, Type, Payload}` for POSTed events:
  types `frame` (JPEG bytes, ≤ 1 MiB), `tab`, `dialog`, `download`,
  `status`. At-most-one outstanding frame per session (latest wins).

**New control-plane routes:**

- `POST /internal/workers/sessions/{id}/browser/events` (worker auth).
- `GET /api/cloud/v1/orgs/{org}/sessions/{id}/browser-view/ticket` (user
  auth) and `GET .../browser-view/stream` (WSS, ticket auth).

**Ticket:** signed, `sessionID`, purpose `browser-view`, 5 min TTL,
single use. Copied from the terminal-ticket implementation; exact signing
key management follows whatever the terminal path already does.

## 6. Security properties

- Chromium CDP binds loopback only; no remote debugging port, no ingress
  port on any provider (D3, D4).
- Frames leave the VM only on the existing authenticated worker channel;
  control plane keeps latest frame in memory only, never persisted.
- Viewer tickets are short-lived, single-use, session-bound (D6); WSS
  rejects expired or reused tickets.
- Per-session profile, cookie, and storage isolation by separate
  `--user-data-dir` (D8), mirroring the Electron partition rule locally.
- All browser-derived content remains untrusted external content; CLI
  markers and UI treatment unchanged.
- No credentials in URLs, ever. The POC's VNC-password-in-query pattern is
  explicitly rejected.

## 7. Resource and provider requirements

- Sandbox shape: ≥ 1 vCPU / 2 GiB (the POC ran headed Chromium in that
  shape; headless is lighter). Estimated 250-450 MiB RSS per session,
  UNVERIFIED; measure in Stage 1 and record.
- VM image must carry Chromium plus the pinned agent-browser linux build
  (same version matrix as `prepare-agent-browser.mjs`). Bake into images;
  do not download per session.
- Providers: docker first (full control). Daytona/ECS/NodeOps/CreateOS
  need only the standard worker channel; no port features required, which
  is the point of D3/D4.

## 8. Staging (supersedes the handoff's stages)

**Stage 1: in-VM automation, no UI.** browserd, Chromium supervision,
agent-browser attach, loopback service with reused backend service code,
D1/D2/D7/D8. Flag `browser_engine=chromium` on docker provider only.
Exit criteria: in a docker sandbox, `open`/`snapshot`/`act`/`screenshot`
return local-shaped JSON via the in-VM CLI; profile isolated per session;
restart policy behaves; measured RSS recorded.

**Stage 2: human view.** Screencast relay, frame endpoint, viewer hub,
tickets, WSS, input injection, D3/D4/D5/D6. Exit criteria: viewer sees the
active tab ≤ 500 ms behind; click and type round-trip on a form page;
expired/reused ticket rejected; frames never persisted (asserted in tests).

**Stage 3: parity extras.** Tabs rail, unseen badge, dialogs, downloads
(surfaced in the Files tree plus a downloads list event), console/errors,
network capture verbs, reconnect resume. Exit criteria: verb-by-verb
comparison table local vs cloud passes; reconnect mid-session does not
lose the browser.

**Stage 4: rollout.** Enable per provider behind the flag as images gain
Chromium; keep `proxy` fallback; publish the shape gate. Deprecation of
the proxy is decided after Stage 4 data, not in this spec.

## 9. Testing

- Unit: service dispatch reuse (backend tests carry over), frame latest-wins
  cache, ticket issue/validate/expire/reuse, chunk-free large screenshot
  path (stays in VM), browserd restart policy state machine.
- Integration (docker provider pattern): end-to-end verb pass against a
  local test page; two-session isolation (no cookie bleed); pause/resume
  tab restore.
- Control plane: httptest fakes for the viewer hub and frame endpoint; no
  network-dependent tests per repo rules.
- CI: `npm run lint`, `npm run frontend:typecheck`, backend and frontend
  builds; worker protocol changes regenerate anything the repo generates
  for cloud contracts.

## 10. Open questions and implementation-time checks

1. Agent-browser engine attach surface: the Electron CDP bridge proves
   attach mode exists; read the pinned binary's CLI to confirm the exact
   flag for "connect to external CDP endpoint" before Stage 1 code.
2. Terminal streaming mechanics in the Cloud client: the ticket pattern is
   copied from it; confirm its transport (WSS vs SSE) and mirror the
   viewer stream accordingly.
3. Screencast behavior on cross-origin iframes and lazy-loaded content:
   verify frame fidelity on a representative page set during Stage 2.
4. Whether `browser_engine` must also gate the existing proxy iframe routes
   (route returns 410 when engine=chromium) or stay parallel; decide at
   Stage 2 review.
5. Measured per-session Chromium RSS on target shapes (fills the D7/section
   7 estimate).

## 11. Risks

- Screencast latency on high-churn pages (mitigation: quality/scale floor,
  ack backpressure; WebRTC is the documented escape hatch).
- Chromium image weight across five providers (mitigation: Stage 4 gates
  rollout per provider).
- Unhandled native interstitials confuse users (documented non-goal; JS
  dialogs and HTTP auth are handled via CDP events).
- Agent-browser engine pinning drift between desktop and VM images (keep
  one source of truth for the version matrix; fail browserd start on
  version mismatch rather than run skew).
