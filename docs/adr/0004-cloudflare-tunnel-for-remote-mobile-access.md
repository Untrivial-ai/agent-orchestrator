# 4. A Cloudflare tunnel for remote mobile access

Date: 2026-08-28
Status: Accepted

## Context

Connect Mobile reached the daemon over the LAN, or over Tailscale if the user
had set it up. Neither works from a phone on cellular, which is the case a
supervision product most needs: the user opens the app because something needs
them, from wherever they happen to be.

The alternatives were an overlay network (Tailscale — already supported, but it
makes the user onboard into a second product), a rendezvous relay of our own
(weeks of work and infrastructure to operate), or a managed ingress tunnel.

## Decision

The daemon supervises a `cloudflared` quick tunnel and advertises its hostname
as one more endpoint the phone races, alongside every LAN address and any
Tailscale address. The phone prefers `lan > tailscale > tunnel`, so the tunnel
carries traffic only when it is the sole working path.

## Consequences

### Reach, for no money and no infrastructure

Cloudflare Tunnel is free, bandwidth included, and a quick tunnel needs no
account. `cloudflared` dials outbound, so there is no inbound port, no NAT
traversal and no certificate problem — the hostname carries a publicly trusted
certificate, which also satisfies iOS App Transport Security and retired the
whole MagicDNS/tailnet-certificate branch of the old pairing flow.

### Cloudflare terminates TLS

This is the substantive trade and it should be understood before relying on the
tunnel for anything sensitive.

The phone's TLS session is with Cloudflare, not with the user's machine: the
certificate is Cloudflare's `*.trycloudflare.com`. Cloudflare decrypts, then
forwards down the tunnel to `cloudflared`, which speaks plain HTTP to
`127.0.0.1`. Traffic is encrypted in transit and **not** end to end; there is a
point inside Cloudflare's infrastructure where it exists as plaintext, because
that is how a reverse proxy routes at all.

On that path Cloudflare is structurally able to observe:

- conversations with the agent — prompts and replies, which is source-code
  discussion;
- terminal input and output, since the mux WebSocket is `wss://` to the same
  hostname and terminates there too. Anything printed to a terminal, including
  environment variables and tokens, passes through;
- project names, branch names, worktree paths, pull-request titles;
- the `Authorization: Bearer` connection password on every request.

"Able to observe" is not "does log" — Cloudflare's own policies govern
retention, and nothing here asserts what they are. But the capability is
structural rather than incidental, and quick tunnels carry an explicit warning
that Cloudflare "reserves the right to investigate your use of Tunnels".

The endpoint preference is doing real work here. LAN traffic never leaves the
network. Tailscale is WireGuard between the user's own devices, and its
coordination server cannot decrypt it. Only the tunnel involves a third party,
and only when nothing else answers.

A named tunnel on a domain we own would fix hostname rotation and possibly the
buffering below. It would **not** change any of this: same termination model,
same visibility. The only thing that does is encrypting payloads before they
leave the device, so whoever forwards them holds ciphertext — which is why a
relay is worth building if this posture ever becomes unacceptable.

### The tunnel buffers HTTP bodies, so SSE cannot stream over it

Measured against a running quick tunnel: response bodies are forwarded in
roughly 128 KB chunks. A 121 KB replay produced nothing at all, while larger
ones arrived in exact multiples of 128 KB (393216 = 3 x 128K, 1703936 = 13 x
128K).

The consequences differ sharply by transport:

| Transport | Over the tunnel |
| --- | --- |
| REST | Fine — a completed response flushes. |
| SSE (`/api/v1/events`) | Broken. A chat event is a few hundred bytes and never reaches the threshold, so it is held indefinitely. |
| WebSocket (`/mux`) | Unaffected — 11 ms round trips for small frames. After the upgrade it is a raw tunneled stream, not a buffered response body. |

This is why terminals felt normal over the tunnel while chat did not: they are
on different transports, and only one of them is buffered. A reply the agent
produced in two seconds went unseen for over a minute, and reopening the screen
surfaced it because that path refetches over REST.

An SSE heartbeat was added — correct practice for keeping connections alive
through intermediaries — but a three-byte comment frame is nowhere near the
threshold and does not address this.

### Polling instead, for now

Two stopgaps compensate, both applying only while the tunnel is the active
endpoint:

- the app's main poll drops from 8s to 2s (`lib/pollInterval.ts`);
- the chat screen polls its conversation, which it otherwise never does,
  relying entirely on the stream (`lib/chat/conversationPoll.ts`).

They cost battery and data, and they turn "over a minute, and only after
reopening the screen" into roughly two seconds.

**The intended fix is to carry conversation events over the existing mux
WebSocket rather than SSE.** The mux already reaches the same daemon, already
carries terminal traffic, already round-trips small frames in 11 ms over the
tunnel, and already has a topic subscription (`{ch: "subscribe", topics: [...]}`).
Moving conversation events onto it removes both stopgaps and needs neither a
named tunnel nor a relay. That is a larger change than the stopgaps and is
deliberately deferred, not forgotten.

### Operational costs

Quick tunnels carry no uptime guarantee, and their hostname changes on every
restart — so a daemon that restarts while the user is away leaves a phone with
a dead address and no LAN to fall back on. A named tunnel is the answer to
that, and would also let the buffering behaviour be re-measured on a supported
path.

Cloudflare is also now in the critical path: an incident there means no remote
access. The direct endpoints remain, so it degrades to same-network operation
rather than to nothing.
