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
- Codex and all eight registered ACP Chat providers are
  owned by authenticated, detached
  per-session hosts. Desktop close, full quit, and updater daemon replacement
  detach and reconnect without relaunching the provider or interrupting an
  in-flight turn; explicit session termination destroys the host. ACP reconnect
  restores the initialized session snapshot, JSON-RPC correlation, pending
  interactions, and an acknowledged prompt journal before replaying the same
  durable turn. Host-accepted approval/input commands close the crash window
  before SQLite projection, and live host adoption preserves the browser bearer
  already held by the provider instead of rotating its verifier. Native
  load/resume remains the repair path after actual host failure; it is not needed
  for live adoption. Installation changes and launch-only credentials do not
  block adoption. Updater warnings use actual controller ownership rather than
  a provider allowlist. Shared process tests cover all eight ACP identities;
  authenticated vendor and platform coverage is tracked separately in
  [the research/evidence note](research/persistent-acp-chat-hosts.md).
- Durable Chat conversations with project-scoped orchestrator continuity,
  session-scoped worker history, bounded history pages, transactional raw-event
  archive/projection, controller-generation fencing, turns, messages,
  activities, approvals, structured input, usage, compaction, and rollback.
- Chat drivers for the user's installed Codex (native app-server), Claude Code
  (claude-agent-acp), Cursor, OpenCode, Droid, Kimchi, Kimi, Pi, OMP, and Qwen.
  Qwen Chat uses native `qwen --acp` and requires Qwen Code 0.16.0 or newer.
  Qwen Code's ACP mode enforces approval modes over `session/request_permission`
  (verified live: a non-read-only shell under auto-edit asks), so AO maps its
  permission modes onto Qwen's (default to Ask Permissions) and admits Qwen Chat
  in every mode. OMP Chat uses native `omp acp` and requires OMP
  15.0.0 or newer. Pi's independently installed pi-acp adapter does not enforce
  approval modes, so AO admits Pi Chat only after the user explicitly chooses the
  per-session bypass-permissions fallback. The binding reuses the existing Pi config environment and auth
  probe and is never downloaded by AO. AO reuses each harness's existing
  binary/auth/environment resolution and does not bundle provider CLIs. Cursor
  is Chat-only until its ACP and TUI conversation ids are proven to share identity.
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
- User-opened standalone and session side shells reconnect across daemon and
  desktop restarts while their runtimes live. Explicit close, confirmed exit,
  and session/worktree teardown remain cleanup boundaries; new trusted command
  and authentication terminals remain scoped to their originating app launch.
- Terminal mux over WebSocket (`/mux`): detached native PTY host for new macOS
  sessions, per-client `tmux attach` for Linux and persisted legacy macOS
  handles, and a ConPTY loopback host on Windows.
- Lifecycle reducer plus reaper (`internal/observe/reaper`).
- Agent adapter platform under `internal/adapters/agent/` (25 adapters) with a
  registry and `ao hooks` activity dispatch.
- Daemon-owned in-memory agent readiness coordination with normalized
  installation/authentication observations, purpose-specific freshness,
  single-flight checks, bounded warm-up/retries, launch-time validation, and
  compatibility projections for older agent inventory/probe clients.
- Codex account management under Settings → Agents. AO reconciles the current
  device-global Codex identity, adds file-backed accounts through an inline
  native login terminal, and shows structured authentication, capacity, usage,
  and confirmed reset-credit facts without parsing credentials. A manual global
  switch atomically changes the device credential while briefly fencing new
  Codex mutations. Running AO Codex controllers and reviewers are never
  interrupted or restarted by account switching; new controllers use the
  selected account, and an existing session can be resumed manually when the
  user wants it relaunched. Native history remains in the normal Codex home.
  Users can sign accounts out and delete inactive signed-out accounts.
- OpenAPI spec generated from Go DTOs; frontend TS types generated from it and
  drift-checked in CI.

### Agent authentication coverage

Authentication observations distinguish five states: `authorized` requires a
source-backed native/provider check accepting the effective credentials;
`configured` means usable local credential/configuration evidence without that
validation; `unauthorized` requires explicit native rejection or trustworthy
expired, nonrefreshable credentials where the provider contract supports it;
`not_applicable` requires a resolved, documented no-auth route; and `unknown`
covers missing, malformed, unreadable, unsupported, or inconclusive evidence.
Binary presence is not authorization. `configured` retains unknown effective
readiness rather than being promoted to ready; launch remains the final check.

The following coverage applies to the current authentication-check paths for
the 25 adapters changed by this work. Claude Code and Codex authentication and
account management are unchanged. Scoped TUI spawn and agent-switch checks use
the effective working directory, environment, configuration, and command
arguments without replacing device-global readiness. This does not add scoped
Chat/restore integration or frontend authentication-recovery behavior.

In the table, local evidence yields `configured` unless an exception is stated.
AWS, Google Application Default Credentials (ADC), and Azure chains are bounded
local evidence checks, not cloud authorization probes. They do not call metadata
services, refresh credentials, contact providers, or execute arbitrary shell
credential helpers by default. Supported native validation commands may contact
their provider. Keychain access uses only source-confirmed service/account
selectors. A dash means no added authoritative check or no-auth branch.

| Adapter | Implemented local evidence (`configured`) | Authoritative results and explicit no-auth routes |
| --- | --- | --- |
| Aider | Home/git-root/workspace YAML and native `--config`/`-c`; dotenv, OAuth key file, environment/CLI keys and `set-env`; pinned model aliases and provider-matched LiteLLM credentials; Bedrock AWS, Vertex ADC, and separate Azure OpenAI/Azure AI key/identity chains. `AIDER_CONFIG` is not a native selector. | No-auth: selected keyless Ollama. LM Studio still requires its key. |
| Agy | Explicit Gemini provider plus `GEMINI_API_KEY`, or browser-mode typed macOS `gemini`/`antigravity` keychain OAuth envelope. Missing provider selection permits browser lookup; unsupported/malformed selection does not. Refreshable OAuth is configuration evidence. | —; expired access-only keychain data remains `unknown`. |
| Amp | Valid `AMP_API_KEY`, selected-server `secrets.json` entry, and JSON/JSONC server/settings routing. An invalid nonempty environment key does not fall back to storage. | Bounded `amp usage --no-color` runs first: successful native balance-check exit authorizes regardless of display text; exact native signed-out failure rejects. |
| Auggie | `--augment-session-json` JSON/path, then `AUGMENT_SESSION_AUTH`, then `.augment/session.json`; typed access token, tenant URL, and scopes, including opaque/service-account tokens. | Trustworthy JWT expiry can reject; no invented refresh field or no-auth route. |
| Autohand | JSON/TOML/YAML workspace settings, profiles, `--set`, environment/provider selection; built-in/custom/extension keys, separate account credentials, OpenAI ChatGPT/xAI OAuth, AWS/ADC/Azure auth-mode chains. Durable `ahc_` account tokens ignore stale expiry. | Supported expired nonrefreshable OAuth/account credentials reject. No-auth: selected Ollama/llamacpp/mlx, validated Blueprint Local GGUF path+SHA, custom `apiKeyRequired:false`, or autohandai local plan. |
| Cline | Selected typed provider settings, CLI keys, source-confirmed provider key environment names, supported OAuth, AWS/Vertex ADC/Azure identity, and structured SAP credentials. Aliases are normalized; endpoints/account identifiers alone are not keys. | Supported OAuth expiry can reject. No-auth: selected default/loopback Ollama or LM Studio. |
| Continue | Typed browser login, `CONTINUE_API_KEY`, or the effective first chat-capable config model key; local/fully qualified secret references resolved through documented dotenv precedence. Bare Anthropic key applies only to no-config onboarding. | —; `--model` does not suppress an existing first file model. Unresolved Hub-only models remain `unknown` without independent Continue account evidence. |
| GitHub Copilot | Ordered supported GitHub token environment variables; selected-account `copilot_tokens`; macOS `copilot-cli` keychain with validated native `host:login`; scoped-environment `gh auth token`; typed BYOK API key/bearer. Classic PATs/arbitrary tokens do not qualify. | No-auth: documented keyless loopback Ollama BYOK on port 11434. Token discovery is not live validation. |
| Crush | Global/project/workspace JSON and safe literal crushrc subset; selected provider keys/OAuth/environment references, top-level environment, AWS/ADC/Azure. Relevant malformed configuration or null auth overrides remain `unknown`. | Supported OAuth expiry can reject. No-auth: selected keyless Ollama or custom OpenAI-compatible endpoint. Shell configuration is not executed. |
| Cursor | Scoped/environment/CLI API key; bounded `status --format json` stored-auth/token evidence. | Source-confirmed successful `getMe` account fields authorize; exact signed-out structure rejects. Token-only output remains `configured`. |
| Devin | `DEVIN_API_KEY` with native `cog_` shape, ACP-only `WINDSURF_API_KEY`, or selected typed `credentials.toml` historical `windsurf_api_key`. | Bounded native `auth status`: exact successful/signed-out output authorizes/rejects. |
| Droid | Factory key, encrypted browser file plus key presence; hierarchical legacy/current/project/runtime settings; selected custom model API key/headers; Bedrock AWS. User/project `apiKeyHelper` is ignored, neither counted nor executed. | No-auth: valid explicitly selected keyless custom endpoint. |
| Goose | Selected provider metadata/environment, `secrets.yaml`, fixed macOS `goose`/`secrets` keychain, typed Gemini/Kimi/xAI/Goose ChatGPT/Copilot/Databricks OAuth caches, AWS/ADC/Azure. Valid custom command-auth declarations are configuration only, never executed. | Supported OAuth expiry can reject. No-auth: selected native local providers or valid custom `requires_auth:false`. Selected empty/malformed keyring data does not fall back to stale file credentials. |
| Grok | Selected TOML model API/environment key, `XAI_API_KEY`, deployment key, or `GROK_AUTH`/selected `auth.json` account map. Unsupported generic token aliases are ignored. | — |
| Kilo Code | Selected provider config keys/environment, legacy `auth.json`, `KILO_AUTH_CONTENT`, current/legacy SQLite credential/account schemas, native `kilo db path` and auth-list counts. Native counts/stores prove configuration only. | No-auth: selected Ollama/LM Studio or harness-hosted public free models. Free OpenRouter models still need a key. |
| Kimchi | Environment/project/global key and endpoint, exact selected harness `auth.json` API/OAuth credentials. | Injected official validator at the default endpoint can authorize/reject; production readiness does not initiate this network probe. Supported OAuth expiry can reject. No no-auth branch. |
| Kimi | Current/legacy model/provider config, explicit key/environment bindings, selected typed OAuth reference, exact Authorization headers, legacy fixed macOS `kimi-code`/`oauth/kimi-code` keychain, provider-configured Vertex ADC. | No-auth: selected legacy `_echo` or `_scripted_echo` only. |
| Kiro | Known stored-account schema from `whoami --format json`; `ksk_` key fallback only in an explicitly noninteractive scope. Native stored-token identity is not live authorization. | Exact `account:null` signed-out result rejects; unknown social-account schema remains `unknown`. |
| Muse | `META_API_KEY`; selected `MUSE_AUTH_PATH` or XDG/home typed `providers.meta` credential file. | Supported expired nonrefreshable OAuth rejects. Keychain-backed storage remains unsupported/`unknown`: source-confirmed selectors are unavailable and are not guessed. |
| OMP | Global/project model roles, selected environment/dotenv, models YAML, read-only enabled provider SQLite rows, `auth.json` fallback, runtime key, AWS/ADC. Broker URL+token is device-wide configuration; selected-provider broker evidence requires a matching usable encrypted snapshot or applicable override, without a broker call. Native active XDG data/cache migration and default profiles are supported. | Supported OAuth expiry can reject. No-auth: selected `auth:none` or Bedrock skip-auth. Nondefault profiles, config overlays, aliases/account pools, ambiguous `--plan`, and multi-selector roles remain `unknown`. |
| OpenCode | Native JSON/JSONC model selection, provider environment/`auth.json`, bounded `opencode auth list`, Bedrock AWS. Auth lists are configuration only; no guessed database discovery. | No-auth: selected Ollama/LM Studio or harness-hosted public free models. |
| Pi | Environment, typed provider environment/OAuth `auth.json`, custom `models.json` API/key, AWS/Vertex. Explicit empty/null/malformed API fields do not inherit a lower-priority value. | Exact native `pi auth check --provider <id>` ready/exit 0 authorizes; not_ready/exit 1 rejects; ambiguous output stays `unknown`. Supported OAuth expiry can reject. No-auth: selected llama.cpp or validated loopback custom provider. |
| Prime Agent | Selected settings/`auth.json`/`models.json`, environment/runtime key, native credentials kept separate from AO-owned runtime directory, AWS/Vertex. Explicit unqualified models must resolve independently, without borrowing the saved provider. | Supported OAuth expiry can reject. No-auth: selected local Ollama or Bedrock skip-auth. No native status probe. |
| Qwen | Typed system/user/project settings, dotenv/environment, selected protocol/model/environment-key binding, CLI keys, Vertex ADC. Retired free-tier OAuth caches are not credential evidence. | No-auth: selected configured loopback OpenAI Chat/Responses route. |
| Vibe | Defaults/user/project TOML, active model, agent profile including AO-scoped profiles, environment and `VIBE_HOME` dotenv selected key. | No-auth: valid selected provider with empty `api_key_env_var`, including llama.cpp. |

Unresolved provider/model routing stays `unknown` rather than borrowing another
provider's credentials or assuming that a local-looking URL needs no auth.
These observations are advisory: locally configured credentials can still be
expired, revoked, or insufficient when the native harness uses them.

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
- Agent pickers consume the normalized readiness snapshot, show cached state
  immediately, and delegate open/focus/selection freshness decisions to the
  daemon coordinator.
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
