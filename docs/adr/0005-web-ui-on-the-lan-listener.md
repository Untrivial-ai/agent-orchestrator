# 5. Web UI on the LAN listener

Date: 2026-08-22
Status: Accepted

## Context

The daemon already ships a network-facing authenticated listener ("Connect Mobile"): Bearer-password auth with per-source lockout, control-route blocking at the socket, and PTY terminals served by the daemon. Native clients use it today.

A browser cannot. There is no web client to load, the existing renderer has Electron-only assumptions, and a browser cannot attach `Authorization: Bearer` to a WebSocket upgrade, an `EventSource`, or the top-level navigation that loads the app.

We want any browser that can reach the daemon to be a first-class client — board, terminals, chat, orchestrator — with no Electron and no desktop GUI, over whatever network path the operator already trusts: a home LAN, a TLS reverse proxy, a tunnel (ADR-0004), or a Tailscale tailnet.

## Decision

Serve the existing renderer, built for a browser target, as static assets from the daemon's existing LAN listener. Browsers log in with the existing connection password and receive a session cookie. Deployments behind `tailscale serve` may additionally opt in to trusting its identity header instead of the password.

**D1 De-Electronize the existing renderer — do not build a new web client.**
`frontend/src/renderer/lib/bridge.ts` is already a single, complete, typed abstraction over every Electron capability, with a working browser fallback and a `nativeCompositionEnabled: false` flag already in it.
*Rejected alternative:* A thin client on `packages/product-ui` + `packages/cloud-client`. `cloud-client` targets the AO Cloud API, not the daemon, and `product-ui` carries leaf components only, so this means re-implementing the entire product UI.

**D2 Serve from the LAN listener — not from a reverse proxy in front of loopback.**
Reusing the LAN listener inherits auth, lockout, and the control-block list for free. It also makes the SPA same-origin with the API, removing CORS from the design entirely. This adds no new listener. AGENTS.md's LAN-listener rule is amended to say the LAN listener may also serve the web UI, following the pattern ADR-0001 used for the loopback rule.
*Rejected alternative:* A Caddy/nginx proxy fronting `127.0.0.1:3001` with zero Go changes. The loopback listener is unauthenticated by design, with protections enforced at the socket; a proxy would have to re-implement that blocklist correctly and forever. (A TLS-terminating proxy in front of the *LAN* listener is fine and supported — see Deployment shapes.)

**D3 Browsers authenticate with a session cookie; trusted identity is an opt-in alternative; Bearer stays for native clients.**
The cookie is the default browser credential and works on every network path. Where the operator already runs `tailscale serve`, trusting its `Tailscale-User-Login` header removes the password from the browser and makes revocation an ACL change. It is off by default and guarded by R7.
*Rejected alternatives:*
- Query-param token: leaks into logs, and `tailscale serve` strips query parameters from WebSocket upgrades.
- Broadening the existing `ao_conn` cookie: it is deliberately path-scoped to `/preview/files/` and must stay that way.

## Deployment shapes

Nothing changes unless the LAN listener is enabled. Once it is, every shape runs the same code:

1. **Direct on the LAN** (default bind `0.0.0.0`). Browsers log in with the connection password. Traffic is plaintext — the same accepted limitation as ADR-0001 — so use this only on a network you trust.
2. **Behind a TLS-terminating proxy or tunnel** (Caddy, nginx, a VPN gateway, the ADR-0004 tunnel). Set `AO_CONNECT_BIND_HOST=127.0.0.1` so the proxy is the only way in, and `AO_CONNECT_STRICT_PORT=1` so the port it targets cannot drift. The proxy must set `X-Forwarded-For` and `X-Forwarded-Proto`; the session cookie is then marked `Secure`, and lockout is keyed per client (R5).
3. **Behind `tailscale serve`** — shape 2 with Tailscale as the proxy. Optionally set `AO_CONNECT_TRUST_TAILSCALE_IDENTITY=1` with an `AO_CONNECT_ALLOWED_LOGINS` allowlist to skip the password login (R7).

## Security rules

Each rule has tests. Code comments cite these labels.

**R1 CSRF.** Cookie- and identity-authenticated requests other than `GET`/`HEAD` are rejected unless `Sec-Fetch-Site: same-origin` or the `Origin` host equals the request `Host`. The cookie is `HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure` when the request arrived over TLS or carries `X-Forwarded-Proto: https`. Bearer requests are unaffected.

**R2 CSWSH.** `/mux` previously skipped origin verification because the daemon bound loopback only. Once a cookie exists on a network listener, that justification is void. `/mux` upgrades are same-origin-checked under cookie or identity auth; Bearer keeps skip-verify, because native clients legitimately send arbitrary `Origin`.

**R3 Subprotocol fallback.** `/mux` accepts `Sec-WebSocket-Protocol: ao.bearer.<token>` and echoes it — the standard escape hatch for a browser whose cookie does not survive an intermediary.

**R4 Configurable bind, strict port.** `AO_CONNECT_BIND_HOST` makes the bind host configurable (default `0.0.0.0`, unchanged). `AO_CONNECT_STRICT_PORT` makes a taken port fail startup instead of falling back to an ephemeral port that a proxy pointed at a fixed port would silently miss (default off, unchanged).

**R5 Unspoofable lockout key.** `middleware.RealIP` rewrites `RemoteAddr` from the client-supplied `X-Forwarded-For`. The password login route is a brute-force target, so its lockout must not be evadable by rotating that header. `X-Forwarded-For` is trusted for the lockout key only when the listener is loopback-bound, where the only non-local path in is a proxy that sets it. Otherwise the key is the transport peer address.

**R6 Session store.** The cookie value is a 32-byte random opaque id, never the password. Sessions live in `~/.ao/web/sessions.json` (mode `0600`, atomic write) and persist across daemon restarts. Sliding 30-day expiry, 90-day absolute. Regenerating the password or disabling the bridge revokes all sessions.

**R7 Trusted identity is opt-in and loopback-only.** A header is spoofable by anything that can reach the socket, so identity trust may be enabled only when the bind host is loopback; the daemon refuses to start otherwise. It requires an explicit login allowlist — an empty allowlist denies everyone. Identity is ambient with no `SameSite` backstop (the proxy attaches it to every request, including one from a hostile page), so R1 and R2 are the entire defense and are tested against identity auth specifically.

**Auth precedence** — one decision point: (1) Bearer, (2) session cookie, (3) trusted identity when enabled.

## Consequences

- The LAN listener gains an unauthenticated surface: `GET /login` and `GET`/`POST /api/v1/web/session`. Everything else stays behind auth.
- Cookie and identity auth are ambient, so CSRF and CSWSH become live risks that did not exist before. R1, R2, and R7 are mandatory.
- Shape 1 inherits ADR-0001's plaintext-on-the-LAN limitation. Shapes 2 and 3 retire it: the plaintext hop is loopback-only and TLS terminates at the proxy.
- The agent workspace preview panel (a session's own dev server) is served on isolated `<base32>.localhost` origins, which a remote browser cannot resolve. Preview is a known remote limitation.
