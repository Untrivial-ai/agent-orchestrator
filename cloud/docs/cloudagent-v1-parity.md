# CloudAgent V1 Parity And Performance Checklist

This document compares the private AO Cloud implementation with the
`cloudagent-v1-auth` branch in the public checkout. "Confirmed in V1" means the
mechanism exists in that branch's source. It does not mean AO Cloud should copy
the old architecture wholesale: AO Cloud is multi-replica and Postgres-backed,
whereas V1's desktop flow is loopback-daemon based.

## Completed In This Release

1. **Durable client projection events**
   - AO Cloud: session streams now include worker activity/readiness, sandbox
     provisioning, workspace changes, PR creation/claim, and review submission.
     The Cloud UI subscribes for the active project, debounces event bursts, and
     refreshes its durable projections from REST.
   - Confirmed in V1: `frontend/src/renderer/lib/event-transport.ts` uses SSE to
     debounce and invalidate workspace and SCM queries.

2. **Replayable event cursors**
   - AO Cloud: the server persists events, accepts `after`/`Last-Event-ID`, and
     resumes the client stream after reconnects.
   - Confirmed in V1: its event endpoint and browser `EventSource` reconnect use
     `Last-Event-ID` replay.

3. **GitHub setup continuation is based on fresh server data**
   - AO Cloud: after personal OAuth completes, the organization-installation
     decision uses the returned connection and installation list, not stale React
     state. The second install window opens only when an organization grant is
     actually required.
   - Confirmed in V1: durable user connection and installation records drive the
     setup flow.

4. **Worker GitHub CLI discovery**
   - AO Cloud: the managed `gh` wrapper now resolves the real binary at
     `/usr/local/bin/gh` or `/usr/bin/gh`, while retaining an explicit override.
     This makes normal `gh pr create` work in the NodeOps image instead of
     requiring a manual token/curl fallback.
   - Confirmed in V1: workers receive brokered GitHub credentials through a
     managed wrapper.

## Parity Checklist

| # | Capability | AO Cloud status | Confirmed in V1 | Follow-up / reason |
|---|---|---|---|---|
| 1 | Durable event storage and replay cursor | Implemented | Yes | Keep retention and cursor-gap metrics visible. |
| 2 | Browser consumes stream for workspace/PR/review projections | Implemented through invalidation plus durable REST refetch | Yes, query invalidation | A typed local reducer can reduce the remaining refetches once event payload versions are stable. |
| 3 | Direct terminal WebSocket URL | Implemented | Yes | Keep this path independent of page-origin proxy routing. |
| 4 | Terminal output resume from sequence | Implemented | Yes | Preserve replay/reset markers and test reconnect after a worker restart. |
| 5 | Per-session terminal pool and duplicate-connect suppression | Implemented | Yes | Keep one pool owner per session/kind; do not let panels open competing sockets. |
| 6 | Workspace operation scheduler | Partial | Not confirmed | Centralize diff, file-list, PR, and terminal-read requests behind a per-session scheduler to prevent operation-limit 429s. |
| 7 | Interactive terminal lease | Implemented | Not applicable: V1 keeps VMs running | Renew only while a user is actively viewing a terminal; expiry must restore normal idle pause. |
| 8 | Idle pause and truthful wake state | Implemented | Different policy: V1 does not idle-pause | Continue distinguishing `waking`, `provisioning`, `shell starting`, and `unavailable`; never render cached output as live. |
| 9 | Bounded parallel session wake on login | Implemented | Not applicable | Keep global, provider, and organization concurrency limits; measure queue time and provider resume latency. |
| 10 | Background session-state reconciliation | Implemented | Partial | Persist and reconcile state even when no inspector is visible; surface one authoritative derived status. |
| 11 | Explicit worker epoch and stale-command fencing | Implemented | Partial | Maintain fenced terminal/work commands so a replaced worker cannot write stale completion data. |
| 12 | GitHub personal OAuth connection | Implemented | Yes | Keep connection state durable and account-scoped rather than workspace-scoped. |
| 13 | GitHub organization installation and repository grants | Implemented | Yes | Confirm webhook/install completion before enabling a repository in project creation. |
| 14 | Fresh brokered worker Git credentials | Implemented | Yes | Rotate per checkout/worker and never persist a user token in sandbox state. |
| 15 | GitHub PR claim, subscription, and review delivery | Partial | Yes | PR claim is available; subscription must use a user-authorized identity where GitHub requires it. Ensure UI reflects durable PR/review facts. |
| 16 | End-to-end telemetry for terminal, sandbox, GitHub, and stream lag | Partial | Partial | Add dashboard/SLOs for ticket rejection, resume duration, event replay lag, GitHub grant failures, and worker credential failures. |

## Recommended Order For Remaining Work

1. Add the per-session operation scheduler in item 6. It is the largest remaining
   protection against terminal/diff/file contention and repeated 429s.
2. Promote event payloads from invalidation hints to versioned projection updates
   for high-frequency status, diff, and PR changes. Keep a periodic REST reconcile
   for correctness after a stream gap.
3. Instrument item 16 before increasing concurrency. A measured queue/resume
   budget is safer than making terminal retries more aggressive.
4. Complete PR subscription with a user identity, not the GitHub App identity.
   GitHub intentionally rejects user-notification changes made by an app token.

## Verification Expectations

- Unit tests cover event filtering, replay, GitHub setup continuation, wrapper
  binary discovery, and projection subscription cleanup.
- The Docker lifecycle smoke verifies control-plane restart, worker replacement,
  and workspace persistence.
- A disposable Postgres integration run verifies event persistence and stream
  replay across the real storage boundary.
- Hosted staging and production verification must additionally check the GitHub
  App callback, an organization repository grant, a worker `gh pr create`, and
  a terminal reconnect against the deployed NodeOps image.
