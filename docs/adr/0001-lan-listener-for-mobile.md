# 1. A second, authenticated, plaintext LAN listener for mobile access

Date: 2026-07-07
Status: Accepted

Implementation note (2026-10-03): this ADR records the original LAN decision.
Current Connect Mobile also advertises managed remote/Tailscale secure endpoints
around the authenticated listener; the primary listener remains loopback-only.
The implementation persists the rotating password in a mode-`0600` file and
derives its comparison hash in memory, rather than persisting only a hash.
See the [current access guide](../../frontend/src/docs/content/configuration/remote-access.mdx)
and [identity-probe ADR](0003-unauthenticated-identity-probe.md). The historical
decision below is retained for context.
The current v2 Pairing QR carries the rotating bearer token, host identity, and all
advertised endpoints in its URI fragment, so the QR or copied link is sensitive. The
mobile app races those endpoints and supports multiple paired desktop records. The
historical non-secret QR and out-of-band password wording below remains the original
decision record.

## Context

The daemon binds `127.0.0.1` only. AGENTS.md carries a hard rule: _"The daemon is
a loopback-only sidecar. Do not make the bind host configurable or expose it beyond
`127.0.0.1`."_ That rule keeps the Loopback Listener safe **without
authentication**. The OS guarantees that nothing off-box can reach it.

We want a physical phone to use the app over the local network. The only prior
mechanism was a standalone Node proxy (`ao-phone-proxy.js`) run by hand, with
IP trust-on-first-connect and no password. The user rejected the proxy approach and
asked for an in-app "Connect Mobile" feature.

Two forces collide. Exposing anything to the LAN removes the loopback safety
guarantee. The target mobile app is **Expo/React Native**, where trusting a
self-signed TLS certificate through fingerprint pinning requires native modules
across three transports (`fetch`, the `/mux` WebSocket, and the xterm WebView).
That is a large, risky effort for the desired scope.

## Decision

Add a **second HTTP listener inside the daemon**, bound to the LAN, gated by auth.
The Loopback Listener is left byte-for-byte unchanged (desktop/CLI stay
unauthenticated). This **overrides the AGENTS.md loopback-only hard rule**, by
explicit user decision on 2026-07-07; AGENTS.md should be amended to scope that rule
to the Loopback Listener.

Security posture:

- **On-demand.** The LAN Listener does not exist until Connect Mobile is enabled;
  disabling closes the socket. It is off by default, so there is no standing LAN
  surface.
- **Single rotating Connection Password**, 8-char alphanumeric, stored only as a
  hash, compared constant-time. Sent as `Authorization: Bearer <password>` on both
  REST and the RN WebSocket (RN's WebSocket header option). Rotating drops the
  current phone.
- **Per-source Lockout** after 5 failed attempts. It is not global, so a hostile
  device cannot lock out the real phone.
- **App API only** on the LAN Listener; daemon-control routes keep their existing
  loopback-only guard (`localControlRequest`) with no change.
- The authenticated app API includes `GET /api/v1/fs/dirs` for remote folder
  selection. Its bounded response identifies the listed absolute path and parent,
  plus each visible subdirectory's name, absolute path, and `.git` presence; it
  does not expose file contents, sizes, timestamps, files, or dotted names.
- **Plaintext transport (HTTP), accepted.** No TLS. The feature is
  **home-network-only** and the UI says so. The Pairing QR therefore carries only
  host+port (non-secret); the Connection Password is delivered out-of-band (read off
  the desktop screen, typed into the phone), so a captured QR alone cannot connect.
- State persists to `~/.ao/mobile/config.json` (atomic write), honoring the
  "all state under `~/.ao`" rule. The listener re-binds on the default port with an
  ephemeral fallback; the QR always reflects the actually-bound port.

## Consequences

- The daemon gains a network-facing, authenticated attack surface whenever Connect
  Mobile is on. Loopback behaviour is unaffected, so desktop/CLI carry no regression
  risk.
- On untrusted networks the Connection Password and all traffic are exposed to
  sniffers. This is an accepted, stated limitation, not an oversight.
- TLS is deliberately deferred. A future upgrade (TLS listener + a `fingerprint`
  field in the Pairing QR + RN cert pinning) is additive: it does not require
  reworking the auth, lifecycle, or persistence chosen here.
- AGENTS.md must be updated so the loopback-only rule reads as scoped to the
  Loopback Listener, or future agents will (correctly) flag this code as a violation.
