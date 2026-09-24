# Cloud review follow-up

## Scope

Fix the resize lockout and false-success delegation replay found during the
remediation review. Make incompatible worker streams report an actionable
upgrade error. Do not perform the separate LOC consolidation or publish changes.

## Browser failure recovery

A rejected viewport must not leave a ready-looking, unclickable surface. End
that viewer connection with a persistent error and the existing reconnect
button. Ignore late frames and controls from the closed connection. Retry
obtains a new ticket and accepts input only after a matching resized frame is
painted. Agent-owned viewport rejection is expected contention: wait for
ownership release rather than interrupting the viewer.

An unsupported protocol or unsafe epoch identifies an incompatible worker.
Stop the connection with instructions to use an updated worker image. Never
round an epoch or disable input fencing to preserve old-worker compatibility.
This is explicit rejection, not support for the older worker protocol.

## Delegation startup checkpoints

Distinguish a committed session identity from completed startup. Persist
`seeded`, `starting`, and `ready` checkpoints separately from the historical
reservation state. Existing reservations retain a legacy marker.

Before external workspace or runtime effects, atomically claim a seeded
identity by changing its checkpoint to starting. A restart before that claim
can finish startup using the same identity. Only one caller wins the claim.
Mark ready only after the manager finishes workspace, runtime, and initial
prompt setup, before reporting success. Both terminal and chat launch paths
must follow this rule.

A retry during or after an uncertain starting phase must report recovery
required with the associated session identity, never success or an automatic
second launch. A runtime may have been created without its handle being
committed. Existing restore cannot safely recover that case, so retain the
record for explicit inspection rather than assuming provider absence.
Daemon reconciliation must preserve these checkpoints instead of deleting an
incomplete session and cascading away its reservation.

Confirmed rollback deletes the seed and its reservation together, allowing a
fresh retry. Completed legacy requests preserve replay behavior. Legacy
pending requests remain guarded. Do not alter merged migrations.
Migration 0157 adds the checkpoint and worker lookup index. Rows created by
the earlier identity-only implementation have uncertain outcomes and migrate
to starting, not ready.

## Acceptance and verification

- Resize rejection remains visible despite late frames; reconnect restores
  input after acknowledgement and paint. Expected control contention recovers.
- Unsafe epochs and mismatched protocols stop with an upgrade error; current
  worker frames and controls continue to work.
- Crash after seed commit resumes that identity. Concurrent claims create at
  most one runtime. Starting rows never replay as success. Completed startup
  replays after restart, and rolled-back creation can be retried.
- Exercise real SQLite transactions and upgrades, manager terminal/chat paths,
  service error mapping, and browser stream/component boundaries.
- Run affected suites first, then backend build, vet, race suite and frontend
  typecheck and full tests. Record gaps instead of claiming desktop or remote
  provider evidence from unit tests.

## Plan

1. Add browser failure recovery and incompatible-worker regression tests.
2. Add durable startup checkpoints, guarded manager claims, and recovery tests.
3. Run verification, review the final diff, and update the report.
