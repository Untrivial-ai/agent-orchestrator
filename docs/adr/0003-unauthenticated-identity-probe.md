# 3. One unauthenticated identity probe on the LAN listener

Date: 2026-08-27
Status: Accepted

## Context

The mobile app is moving from a single stored address to a list of endpoints it
races: every private IPv4, every Tailscale address, and a managed tunnel. The
phone probes them concurrently and keeps the first that answers.

A private address is not an identity. `192.168.1.42` exists on most home and
office networks, and it is a different device on each of them. A phone that
paired at home and later joins a café network will find *something* at the
address it remembers.

Verifying which machine answered therefore has to happen **before** a credential
is presented. If the phone must authenticate in order to learn who it is talking
to, then on a foreign network it sends `Authorization: Bearer <connection
password>` to whatever device holds that address. Per-device tokens (planned)
shrink the blast radius to one device but do not remove the leak.

This collides with the AGENTS.md hard rule that the Connect Mobile LAN listener
runs **only** behind the bearer-password `authMiddleware`
(see [0001](0001-lan-listener-for-mobile.md)).

## Decision

Exempt exactly one route from `authMiddleware`: **`GET /api/v1/identity`**.

It returns an opaque, installation-bound host id and the mobile contract version,
and nothing else:

```json
{ "hostId": "h_b3e07f31-4803-46ac-bced-ded38a0fff71", "apiVersion": 1 }
```

The exemption is deliberately narrow, and there are tests for each constraint:

- **Exact path match**, not a prefix — nothing nested below `/api/v1/identity`
  inherits it.
- **`GET` only** — a write to the same path still requires the password.
- **Checked ahead of the lockout** — a phone racing several endpoints must not
  be able to lock itself out by probing, and an unauthenticated probe carries no
  credential to get wrong.

The host id is persisted at `~/.ao/mobile/identity.json` and belongs to that AO
data directory. Hardware fingerprints are retained as diagnostic metadata but
never rotate the id: docks, OS interface changes, and replacing the only network
card must not silently unpair every phone. Resetting identity is explicit by
removing this file while AO is stopped; the next start issues a new id.

This means copying the complete `~/.ao` directory copies the identity too. That
is the chosen tradeoff: predictable pairing across ordinary hardware changes
instead of inferring identity from mutable network hardware. A copied data
directory can pass the pre-auth identity check, so backups and migrations of
`~/.ao` must be protected like the rest of AO's credentials and state.

## Consequences

**This narrows the overall attack surface rather than widening it.** The route
exists to stop the app leaking a credential to an unknown device, which is a
worse exposure than disclosing an opaque identifier.

What an unauthenticated caller on the LAN — or anyone who reaches the tunnel
hostname — can now learn: that an AO daemon is present, and a random opaque id
for it. They could already infer the first from the shape of the 401. The id is
not a secret and carries no hostname, user, project, or platform. It does not
authorize requests; the connection password is still required.

AGENTS.md's LAN listener rule is amended to name this single exemption. Any
future unauthenticated route needs its own ADR; this one is not a precedent for
a general exemption mechanism.

An alternative — returning the host id inside the 401 envelope — was rejected:
it overloads an error shape AGENTS.md describes as locked, and puts identity
somewhere no caller would think to look.
