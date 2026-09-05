# 3. Persistent provider hosts for Chat sessions

Date: 2026-08-26
Status: Accepted (Codex and ACP profiles implemented)

## Context

Chat controllers historically spawned a provider process (`codex app-server` or
an ACP adapter) as a child of the daemon. Desktop close, full quit, and updater
restart all stop that daemon. `Chat.Service.StopAll` therefore closed every
provider pipe, interrupted any active generation, and the replacement daemon had
to launch a new provider process and invoke native resume. The durable thread was
preserved, but the process and in-flight turn were not. TUI sessions did not have
this failure because tmux/conpty already owns their harness outside the daemon.

## Decision

AO will move native Chat provider process ownership into a detached, per-session
host. The daemon is an authenticated, exclusive client of that host, not its
parent lifetime owner.

Codex app-server uses the original raw protocol profile:

- `ao chat-host` launches the provider in the session worktree and listens only
  on an ephemeral loopback TCP port. A 256-bit capability and protocol version
  live in a mode-0600 descriptor below `~/.ao/data/chat-hosts/<session>/`.
- Exactly one controller may attach. The host retains provider stdin/stdout when
  that client disconnects, so an active turn continues. A replacement daemon
  authenticates, takes the exclusive attachment, and does not repeat
  `initialize` or `thread/resume`. If updater processes overlap, the replacement
  waits for the old attachment to release instead of launching a rival provider.
- Provider frames produced while detached are replayed in order from a bounded
  32 MiB buffer. At the bound, the host applies backpressure instead of silently
  losing protocol output. Unanswered provider-to-client requests are retained
  even if a previous daemon received them, then replayed until a controller
  response is forwarded, so an approval cannot disappear across detach. Codex
  native history remains the repair source after host/provider failure.
- The host records the greatest numeric client request id it forwarded. A new
  controller starts above that high-water mark, preventing a late provider
  response from correlating with a replacement request.
- Controller-generation checks in SQLite remain the projection fence. The
  transport additionally rejects concurrent attachment, preventing two live
  daemon controllers from writing the provider connection.
- Normal daemon shutdown—including window-close supervisor shutdown, full app
  quit, and updater handoff—detaches. Explicit session kill, controller
  replacement, or durable orphan reconciliation sends authenticated shutdown.
- Deliberate detach does not project `ActivityExited`, settle the active turn, or
  fail pending input. A live reconnect also skips the native-history settled
  barrier; buffered protocol events continue the turn immediately on the same
  initialized connection.
- Startup orphan reconciliation only destroys a compatible host when durable
  state proves its Codex Chat session is terminated or absent. An unreadable
  store, incompatible descriptor, live PID with an unreachable endpoint, or
  failed auth is preserved rather than treated as death.
- Branch activation first fences an idle source controller, then explicitly
  terminates its host before opening the replacement. If replacement fails, AO
  restores the source branch through native resume. This keeps one provider
  writer at a time and never treats an attached host as permission to launch a
  competing direct process.

ACP providers use a stateful profile on the same control plane. The host remains
provider-neutral above JSON-RPC, but it becomes the logical owner of
connection-scoped ACP state that a replacement SDK client cannot infer:

- Daemon request IDs are remapped to host-owned monotonically increasing wire
  IDs. Responses and `$/cancel_request` targets are translated back to the
  current attachment, so a replacement SDK may restart its local counter without
  colliding with an older request.
- Successful `initialize` and session setup results are cached with the provider
  session ID. A replacement daemon reconstructs capabilities and config state
  from that snapshot; it does not send a second `initialize`, `session/new`,
  `session/load`, or `session/resume` to the same connection.
- `session/prompt` remains owned by the host after the issuing daemon detaches.
  Prompt updates receive stable event identities and are appended before delivery
  to a mode-0600, 256 MiB per-prompt journal under the session host directory.
  Replay is released only after the replacement service restores the durable AO
  provider-turn ID. Persistent ACP delivery is lossless at the driver event
  boundary; journal/quota errors fail explicitly instead of dropping deltas.
- The prompt result is retained until the controller acknowledges its event ID
  after SQLite projection commits. A daemon killed before commit gets the event
  again; a daemon killed after commit but before ACK deduplicates it and closes
  the pending ACK window without projecting a second terminal event.
- Concurrent provider-to-client requests such as permissions and elicitation are
  retained by the host and receive host-stable interaction IDs. Replaying a
  request therefore restores the same durable AO approval/input and its response
  is forwarded once using the provider's original JSON-RPC ID.
- Before releasing a blocked provider request, the daemon records the user's
  approval or input as an idempotent host command. The host assigns the durable
  event ID and replays the request, accepted command, then terminal prompt result
  in causal order. A replacement daemon can therefore finish the original
  provider response and project the resolution exactly once even if its
  predecessor died between host acceptance and the SQLite commit.
- The shared ACP implementation is enabled for Claude Code, Cursor, and OpenCode,
  whose real restart gates passed. Other ACP bindings stay daemon-owned until
  their own authenticated provider matrix passes. Provider-specific launch
  arguments, permission policies, versions, environment, and native recovery
  capabilities remain in their existing bindings; persistence does not broaden
  any provider permission.

The host inherits the already-resolved provider environment once at launch; no
credentials are written to its descriptor. Launch-only credential preparation
is deferred until host adoption has failed conclusively and a new provider must
be spawned. A live reconnect therefore leaves the stored verifier matching the
bearer held by the surviving provider. Possession of the descriptor capability
grants control, so its directory and file permissions are part of the security
boundary. The daemon never exposes this transport through its HTTP API.

## Compatibility and update handoff

The descriptor protocol is explicitly versioned and includes a fingerprint of
the provider worktree, protocol, argv, and environment. A new daemon attaches
only to the exact supported version and matching launch configuration, leaving
an incompatible live host untouched. This fails closed—without spawning a
competing provider—until an explicit compatible handoff or session termination
occurs.

The ACP journal protects daemon replacement, including forced daemon death; it
does not make the provider recoverable after the host or machine itself dies. A
conclusive host failure still launches a new provider and uses the binding's
native `session/load`/`session/resume` support. The journal is removed with the
host and is not a second provider-history database.

## Consequences

- Closing/updating AO no longer terminates Codex or an enabled ACP Chat harness or
  interrupts its active generation. Reopen latency loses provider launch,
  initialization, and native resume; it retains daemon reconciliation and
  native-history repair.
- The detached host is a new local process and capability-bearing control plane
  that must remain backwards-compatible across desktop updates.
- TUI lifecycle is unchanged: tmux/conpty remains its external owner. TUI↔Chat
  handoff remains capability-gated by shared native conversation identity and is
  separate from transport persistence.
- Host crashes still require ordinary native resume. In-memory replay protects
  Codex daemon replacement; ACP's disk-backed prompt journal protects the same
  boundary but not host or machine failure.
