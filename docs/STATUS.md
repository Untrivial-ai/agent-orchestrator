# agent-orchestrator status

Current `main` ships a working single-user local loop: the Go daemon and the
Electron/React frontend both drive a live daemon over HTTP/SSE/WebSocket. The
core GitHub flow works end-to-end: add project → spawn session/orchestrator →
attach terminal → observe PR → merge.

This file tracks progress. For what the product _is_ and how to run it, see the
top-level [`README.md`](../README.md); for the backend mental model see
[`architecture.md`](architecture.md).

## Build & test

The local gate is the backend Go build and race-enabled test suite:

```bash
cd backend && go build ./... && go test -race ./...
```

`npm run lint` (from the repo root) runs `go test ./...` plus golangci-lint.
Frontend checks live under `frontend/` (`npm run typecheck`, `npm run build`).
See [`AGENTS.md`](../AGENTS.md) for the regen workflow when touching the API
surface (`npm run sqlc`, `npm run api`).

## Shipped

### Backend (Go daemon)

- Loopback-only HTTP daemon (chi router, CORS, per-request timeout,
  `/healthz` / `/readyz` / `/shutdown`).
- SQLite store with goose migrations and sqlc-generated queries; DB
  trigger-based change-data-capture into `change_log`.
- CDC poller + broadcaster feeding in-process subscribers and the SSE stream
  at `GET /api/v1/events` (with `Last-Event-ID` replay).
- Full session lifecycle over HTTP: list, get, spawn, kill, restore, rename,
  rollback, cleanup, send, activity, PR claim/list. Orchestrator routes
  (list/spawn/get) are wired too.
- One daemon-committed interface per session. TUI sessions retain the established
  tmux/conpty agent runtime; Chat sessions use runtime-less native controllers,
  persist provider conversation identity, and dispatch lifecycle reactions
  through the same mode-aware session manager. A durable, capability-gated
  drain/interrupt handoff can move the same Claude Code or Codex native
  conversation between TUI and Chat without changing the AO session/worktree;
  rollback, restart recovery, controller-generation fencing, and a transition
  message outbox preserve the one-controller invariant.
- Durable Chat conversations with project-scoped orchestrator continuity,
  session-scoped worker history, bounded history pages, transactional raw-event
  archive/projection, controller-generation fencing, turns, messages,
  activities, approvals, structured input, usage, compaction, and rollback.
- Chat drivers for the user's installed Codex (native app-server), Claude Code
  (claude-agent-acp), OpenCode, and Droid. AO reuses each harness's existing
  binary/auth resolution and does not bundle provider CLIs.
- Project CRUD plus per-project config (`PUT /projects/{id}/config`).
- PR action engine wired into the API: `POST /prs/{id}/merge` and
  `/prs/{id}/resolve-comments`.
- Review routes registered: `GET /reviews`, `POST /reviews/execute`,
  `POST /reviews/{id}/send`.
- Interactive reviewer panes for Aider, Agy, Amp, Auggie, Autohand,
  Claude Code, Cline, Codex, Continue, GitHub Copilot, Crush, Cursor, Devin,
  Droid, Goose, Grok, Kilo Code, Kimchi, Kiro, Kimi, OpenCode, Pi, Qwen, and Vibe. Pi uses an AO-data-owned extension with built-in/project
  resources disabled, structured read-only inspection/reporting tools, and
  Escape-based turn cancellation. Kiro also uses its native Escape
  cancellation. Continue, Qwen, and Vibe also use Escape cancellation. Agy,
  Continue, Devin, Droid, Goose, Kimchi, Kimi, Qwen, and Vibe are explicitly experimental and host-trusted. Grok, Crush, Auggie, Cline, and Autohand are experimental user-approved reviewers that retain their native approval prompts instead of receiving broad unattended flags:
  native modes, autonomous settings, and prompts are not OS or network containment.
- The provider-neutral interactive-reviewer capability gateway and neutral
  AO-owned working-directory contract are available. The experimental
  host-trusted adapters remain candidates for future contained execution once
  their documented sandbox, environment-replacement, broker, and gateway
  prerequisites are implemented.
- Durable dashboard notifications for `needs_input`, `ready_to_merge`,
  `pr_merged`, and `pr_closed_unmerged`: backend enrichment/persistence,
  cursor-paginated read/unread history, live notification stream, and read
  acknowledgement API.
- SCM observer (`internal/observe/scm`) wired into the daemon: GitHub provider,
  lazy/non-blocking auth, per-PR polling with ETag guards and semantic diffing,
  feeding PR facts into lifecycle, which sends agent nudges for CI failures,
  review feedback, and merge conflicts
  ([#75](https://github.com/aoagents/agent-orchestrator/issues/75),
  [#108](https://github.com/aoagents/agent-orchestrator/issues/108),
  [#109](https://github.com/aoagents/agent-orchestrator/issues/109)).
- Terminal mux over WebSocket (`/mux`): per-client `tmux attach` PTY on
  Darwin/Linux; conpty loopback pty-host on Windows.
- Lifecycle reducer plus reaper (`internal/observe/reaper`).
- Agent adapter platform under `internal/adapters/agent/` (25 adapters) with a
  registry and `ao hooks` activity dispatch.
- OpenAPI spec generated from Go DTOs; frontend TS types generated from it and
  drift-checked in CI.

### Frontend (Electron + React)

- Electron + React 19 + TanStack Router/Query + Tailwind + shadcn primitives.
- Target-isolated per-session browser-control spike: a dedicated local
  daemon↔Electron bridge drives only the selected session's `WebContentsView`
  through Electron's bound debugger transport. `ao browser` supports open,
  compact accessibility snapshots and refs, click/fill/type, keyboard input,
  hover and non-mutating element highlighting, scrolling, selection and checked
  state, property reads, stable logical tabs and captured popups, a compact
  user-facing tab selector for switching/closing tabs and popup notices, waits,
  including load/disappearance/DOM-stability conditions, screenshots, console
  messages, page errors, and explicit temporary network-metadata capture while
  the Browser panel is hidden. Network capture is off by default, tab-scoped,
  bounded, automatically expires, and omits bodies and sensitive values. Tabs
  within one worker share an ephemeral Electron profile; different workers
  have isolated cookies and web storage. The browser tab menu is only a tab
  navigation control: it does not render a global activity pill or a
  tab-specific agent marker. Annotation progress is separate and its
  successful-delivery confirmation clears automatically.
- Chromium's official DevTools frontend is available from the direct Browser
  toolbar button, `Ctrl+Shift+I` (Cmd+Option+I on macOS), the titlebar View menu,
  and `ao browser devtools`. It opens in a detached desktop window with normal
  OS close controls and is attached through the same worker-scoped CDP
  multiplexer as the agent, so Elements, Console, Network, Sources, and other
  DevTools panels can remain open while agent automation continues. The
  user-facing DevTools connection is unrestricted; agent CDP commands remain
  policy-limited.
- Preview targets are explicit: `ao preview`, `ao preview <target>`, or
  `ao preview start` selects what the panel shows. The desktop poller no longer
  auto-discovers a static entry point merely because a fresh worker exists.
- Real daemon wiring via the generated `openapi-fetch` typed client
  (`src/api/schema.ts`); mock data only in `VITE_NO_ELECTRON` web-preview mode.
- Electron main handles daemon discovery, launch, and status reporting.
- Shell: sidebar (projects + sessions, add/remove project), sessions board,
  session view + inspector, project settings, pull-requests page,
  spawn-orchestrator flow.
- SessionView renders from the session's persisted mode: the existing terminal
  surface for TUI, or the durable Chat timeline/composer for Chat. Chat retains
  access to session-scoped worktree shells without creating an agent tmux pane.
- Compatible Claude Code and Codex sessions expose an in-session “Open Chat” /
  “Open Terminal UI” action. Chat→TUI is the recovery path and always fences
  queued work before interrupting the active turn; a busy TUI→Chat switch offers
  the explicit finish-and-drain or stop-and-interrupt choice. Both directions
  show durable progress/recovery state.
- Desktop status and SCM summary V1: session status comes from
  `GET /api/v1/sessions`; visible/active PR context comes from
  `GET /api/v1/sessions/{sessionId}/pr`; `GET /api/v1/events` is kept open as
  an invalidation stream rather than a full PR payload stream.
- Concise PR summaries include PR identity, CI state with failing check names
  and links, human reviewer IDs/counts/links for unresolved review comments,
  and mergeability reasons. Raw CI logs and review comment bodies are
  intentionally not part of the desktop V1 API/UI.
- Terminal pane (xterm) over the mux WebSocket, with a live SSE events
  connection and port-rebind on daemon restart.
- Chat history uses bounded pages and targeted CDC/SSE invalidation rather than
  polling and transferring the full lifetime of a conversation.
- In-app notification center with click access, Unread/All filters, paginated
  REST catch-up, live notification stream updates, separate PR/session target
  actions, persistent read history, mark-read controls, and Electron app toasts
  while the app is running.

### Mobile (Expo + React Native)

- Connect Mobile pairs with the daemon's opt-in authenticated LAN listener; the
  loopback listener and its security model remain unchanged.
- New mobile workers and orchestrators request Chat mode by default. Worker
  creation filters to the daemon-advertised Chat harnesses, while Terminal UI
  remains an explicit compatibility choice and typed Chat preflight failures
  offer that fallback.
- Session routing uses the same daemon-committed mode as desktop. TUI keeps
  the existing authenticated mux/xterm surface; Chat uses the same durable,
  paged conversation projection and CDC/SSE invalidation stream as desktop.
- Mobile exposes the same capability-gated TUI↔Chat handoff, busy-turn policy,
  cancellation window, progress overlay, and automatic renderer swap after the
  daemon commits the new controller.
- Native Chat includes prose/Markdown, provider activity, commands, plans,
  changed files, approvals, structured input, model/effort/provider controls,
  compaction, rollback, MCP recovery, skills and file references, staged/native
  image delivery, embedded text resources, voice dictation, retryable delivery,
  persisted drafts, and a session-scoped worktree shell through the existing
  terminal mux.

## In flight / not yet a runtime feature

- **Browser automation acceptance**: the runtime implementation is complete.
  AO packages one
  checksum-pinned Vercel `agent-browser` Rust binary and routes a deliberately
  limited semantic command set through an authenticated, worker-scoped CDP
  bridge to the existing AO Preview. The binary is prepared automatically for
  desktop development and releases and is the single engine behind ordinary
  `ao browser` inspection and interaction commands. AO retains only its
  sanitized network observer and temporary highlight cleanup as safety/UI
  plumbing. Focused checks and a fresh Windows x64 package pass; macOS/Linux
  packaging and manual lifecycle acceptance remain release verification work.
- **Cross-interface raw terminal history import**: compatible providers now
  replay settled native history with stable identities (`thread/read` for Codex,
  ACP `session/load` where advertised), and AO imports it idempotently before
  activating Chat. ACP `session/resume` preserves model context but does not
  replay history, so a TUI→Chat handoff fails closed for resume-only agents.
  AO deliberately does not reconstruct PTY scrollback as messages/tool cards;
  arbitrary terminal bytes are redraw artifacts, not canonical provider events.
- **In-flight tool portability**: drain can finish accepted work and interrupt
  can cancel it, but no common provider protocol serializes a currently executing
  tool call or detached background process for adoption by another controller.

- **Tracker lane**: GitHub tracker adapter exists, but there is no daemon
  observer loop or agent-lifecycle→issue mirroring yet, so the tracker does
  nothing at runtime ([#112](https://github.com/aoagents/agent-orchestrator/issues/112)).
- **Full raw PR/tracker fact surfacing**: the SCM observer writes facts and the
  desktop consumes concise PR summaries, but exposing the full raw `pr_*` /
  `tracker_*` CDC events to live consumers
  ([#110](https://github.com/aoagents/agent-orchestrator/issues/110)) and in
  `ao session get` ([#111](https://github.com/aoagents/agent-orchestrator/issues/111))
  is still open.

Tracking milestone:
[`rewrite`](https://github.com/aoagents/agent-orchestrator/milestone/1).
