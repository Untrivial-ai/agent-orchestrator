# 5. Remote hosts: renderer-side fan-out behind a per-host loopback proxy

Date: 2026-08-24
Status: Accepted

> Numbering note: `0001`-`0004` are taken. This one claims `0005`.

## Context

The desktop app talks to exactly one daemon: the loopback one it starts. A user who
runs AO on more than one machine — a laptop and a workstation, say — can only watch
one of them at a time, and switching means pointing the app somewhere else and
reloading.

The pieces to reach another machine already exist. `docs/adr/0001-lan-listener-for-mobile.md`
added a second, opt-in, password-gated LAN listener so a phone can reach the daemon,
and the `ao` CLI already targets it with `--url`, reading saved hosts from
`~/.ao/remotes.json`. What is missing is a desktop client that can hold several
daemons open at once.

Three forces shape the design:

- **The renderer is a browser.** `EventSource` and `WebSocket` cannot set an
  `Authorization` header, and the daemon's LAN listener requires one. Header
  injection through Electron's `webRequest` fights Chromium's own CORS enforcement.
- **Ids are not globally unique.** A project id is `filepath.Base(path)` on every
  machine, so two machines that both cloned `agent-orchestrator` both call the
  project `agent-orchestrator`, and both number their sessions from one. Any design
  that routes on a bare id acts on whichever daemon answers.
- **Partial connectivity is the normal state.** With N machines, "two fine, one
  asleep" is not an error path; it is Tuesday.

## Decision

**Fan out in the renderer, over a per-host authenticated loopback proxy in the main
process.** No daemon ever talks to another daemon.

1. **Local is a host.** `LOCAL_HOST` is a host id like any other — the one whose
   requests skip the proxy. No code path asks "is this remote?".

2. **Host-qualified refs are the target addressing rule.** Ids are never rewritten;
   renderer reads and writes should take a `Ref = {host, id}` and dispatch through
   `clientFor(ref.host)`. Routes are host-qualified — `/host/$hostId/session/$sessionId`,
   `/host/$hostId/project/$projectId` — and cache keys carry `refKey(ref)`. This is not
   fully enforced by the current types: `HostId` is a plain string, and some IPC paths
   still accept bare ids. In particular, editor handoff sends only `sessionId`, which
   the main process resolves against the local daemon. Until those paths take a
   host-qualified ref, wrong-host routing remains a runtime risk rather than a type
   error.

3. **The proxy solves the header problem.** Electron's main process is not a browser:
   it can set `Authorization` and is not subject to CORS. The renderer talks to
   `http://127.0.0.1:<ephemeral>/<128-bit token>` and the proxy forwards upstream with
   the Bearer credential, stripping the token so the remote daemon and its logs never
   see it. The token lives **in the URL path** — the one place `EventSource` and
   `WebSocket` can both carry a credential. Preflight is answered locally, so the
   renderer's origin never reaches the daemon.

4. **Streams and terminals follow hosts.** One SSE connection per host, each tagged so
   an event from host B invalidates only that host's cache key; a busy remote cannot
   make the local board refetch. The terminal mux pool is keyed by host, and each
   mux URL is derived from that host's own base — keeping the path prefix, which is
   what carries the token.

5. **Failure as data is the target.** Once a host is connected, a failed workspace
   query renders as a labelled section with a retry and never discards another host's
   data. Hosts connect **after** first paint and probes are bounded, so a sleeping host
   cannot delay startup. The current initialization path still omits a saved host when
   its initial connection fails, so that case has no section or in-place retry yet.

6. **Saved credential containment.** Add and edit forms necessarily hold the password
   the user types in renderer state and send it to the main process over IPC. The main
   process never sends a saved password back: saved-host views contain only
   `{label, url}`, and connecting returns the loopback proxy base.

## Trade-offs

| Decision | Why | Cost accepted |
| --- | --- | --- |
| Renderer-side fan-out, not a daemon hub | No daemon-to-daemon link; peer credentials stay in the app rather than behind a loopback socket every local process can reach | Every query, id and write in the renderer became host-aware — a wide, shallow refactor |
| Loopback proxy rather than header injection | `EventSource`/`WebSocket` cannot set headers; `webRequest` mangling fights CORS enforcement | One local listener per connected host, and token-in-path complexity |
| Token in the URL path | The only place both stream transports can carry a credential | A token can leak through logs if a path is ever printed; mitigated by stripping before forward and never logging request paths |
| Ids qualified, not rewritten | A project keeps the same id in its own daemon, CLI and URLs — no translation layer | Composite keys wherever an id is a map or React key |
| Failure as per-host data | With N hosts, partial connectivity is the normal state | Every consumer must read per-host status; getting this wrong once silently hid a failing host |
| Hosts connect after first paint | An unreachable host must never block startup | Consumers must tolerate hosts arriving asynchronously; getting this wrong silently disabled live updates for late arrivals |

## Alternatives considered

1. **Hub federation in the local daemon** — the local daemon connects to peers and
   re-exposes their state as its own; the renderer stays a single-daemon client and
   the header problem disappears. **Rejected:** it moves peer connection passwords
   into the daemon, which loopback clients reach with ambient authority, so any local
   process could transitively reach the whole fleet. It also puts UI-shaped merging
   decisions in Go and introduces the first daemon-to-daemon link in the system.
   Revisit if multi-host becomes a server-side concern.
2. **One active host at a time** — pick a host, the app reloads pointed at it.
   **Rejected on use:** it makes cross-machine comparison impossible and forces a
   reload per switch, which is the workflow the feature exists to remove.
3. **SSH transport** — auth by existing SSH keys, no exposed listener on the remote.
   **Deferred, not rejected;** see below.
4. **Browser-only remote access** — point a browser at the LAN listener. Already
   available and retained, but it is one host per tab with no cross-machine view.
5. **Global id namespacing** — rewrite ids to `host:id` everywhere. **Rejected:** it
   breaks parity with each daemon's own CLI and URLs, and the collision only needs
   solving at the addressing boundary.

## Security review

Scope: the loopback proxy, the per-host token model, the credentialled request path,
and custody of `~/.ao/remotes.json`. The daemon's LAN listener and origin policy were
reviewed separately, with ADR 0001.

**The token model holds.** 128 bits from a CSPRNG, compared in constant time,
per-activation and never persisted, stripped before forwarding, bound to `127.0.0.1`,
and torn down on disconnect. Nothing in the remote path logs a base URL, and the
main process never sends a saved connection password to the renderer.

Four issues were found and fixed, each with a test that fails on the pre-fix code:

| Severity | Finding |
| --- | --- |
| High | A renderer-supplied path could redirect the credential off-host: concatenating a path beginning with `@` turns the saved base into userinfo, so `http://box:3011` + `@evil.example/` is a request *to* `evil.example` carrying the Bearer credential. The origin is now asserted before the request is made. |
| High | An `https://` host was talked to in cleartext: the port was computed for TLS but the connection was made with the plain HTTP client, putting the connection password on the wire unencrypted. |
| Medium | The proxy dropped the host URL's path prefix, delivering credentialled requests to whatever else a reverse-proxied vhost serves. |
| Medium | The saved-host file was unreadable on Windows, where every writable file reports `0o666`; the permission check refused every file and took saved hosts down with it. The CLI already carries this exemption. |

**Accepted risks.**

- **Loopback is ambient authority.** Any local process can reach the proxy port; the
  token is what stops it. This is the same model as the daemon's own loopback
  listener, and the token narrows reach from "any local process" to "a process that
  can read renderer memory".
- **The token is visible in the renderer DOM.** Attachment images are `<img src>`
  against the proxy base. This is not a new class — such an attacker can already
  call the IPC surface — but it does mean the token is not a defence against a
  compromised renderer.
- **`cookie` and `referer` are forwarded upstream**, deliberately: the daemon uses a
  cookie for browser-served preview routes. No document is ever loaded *from* a proxy
  base, so no `Referer` carries the token.
- **Plaintext HTTP on the LAN path** remains the largest limitation. Binding the
  listener to Tailscale, or tunnelling over SSH, removes it today; TLS with pinning
  is the longer-term answer for clients that can do neither.

**Saved hosts stay a `0600` plaintext file, not an OS keychain.** The keychain does
not earn its cost here: the file is deliberately shared verbatim with the `ao` CLI,
so "which hosts exist" has one source of truth, and Electron's `safeStorage` is
unreadable from Go. The protection is also macOS-only in practice — Windows DPAPI and
Linux `safeStorage` are same-user decryptable by construction, and Linux without a
secret service degrades to obfuscation. Finally it does not shrink the blast radius
that matters: an attacker running as the user already has the worktrees, the session
store, and a local daemon that can drive every connected remote. Revisit if AO targets
shared machines, or if the desktop app stops sharing the store with the CLI.

## SSH transport

An `ssh -N -L` forward goes **behind the existing proxy seam with no change to the
proxy**: a credentialled REST request and a WebSocket upgrade both traverse renderer →
proxy → ssh → far side, with the Bearer injected in main and the renderer origin still
stripped. The expensive part is not the transport; it is owning and supervising the
`ssh` child process across three platforms.

So the recipe ships as documentation first — a user can open the tunnel by hand today
and add `127.0.0.1:<local port>` as an ordinary host — and app-managed tunnels are a
later, separately sized step. SSH does not subsume TLS: it covers desktop-to-desktop
only, and a phone cannot spawn `ssh`.

## Consequences

- The renderer is now host-aware end to end: reads, writes, routes, caches, event
  streams and terminals all carry a `Ref` or a host id. New code that addresses a
  session or project by bare id is a bug, and the type system says so.
- Adding a transport means adding a way to produce a base URL for a host. Everything
  above the proxy already works in terms of "this host's base".
- The feature is off unless a host is saved and connected. With no remote host the
  fan-out is a loop of one and the app issues exactly the requests it did before.
