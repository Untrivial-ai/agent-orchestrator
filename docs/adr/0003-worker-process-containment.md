# ADR 0003: Contain tmux worker processes in an OS-owned boundary

- **Status:** Proposed
- **Date:** 2026-08-04
- **Issue:** [#2523](https://github.com/Untrivial-ai/agent-orchestrator/issues/2523)

## Context

The tmux runtime currently records each pane's POSIX session ID and uses
`pkill/pgrep -s` during destroy. A descendant that calls `setsid` leaves that
session, so the pane can disappear while the descendant continues to run.
That makes a successful tmux teardown insufficient evidence that the worker's
processes were released.

## Decision

On Linux, an explicitly selected `AO_PROCESS_CONTAINMENT=systemd` backend puts
the pane command in a deterministic transient systemd user scope. The scope is
configured with `KillMode=control-group`, an explicit stop grace, and
`SendSIGKILL=yes`. Destroy and restart stop the exact scope and accept success
only after systemd reports it inactive/dead or unloaded. The existing tmux
session-ID reaper remains the default and compatibility fallback.

The first implementation slice covers the successful Create, Restart, and
Destroy lifecycle. It does not claim durable cleanup facts, restart
reconciliation, retry persistence, Docker cleanup, or resource limits.

## Consequences

- A `setsid` descendant remains owned by the systemd scope and is terminated by
  the scope's control-group policy.
- The opt-in backend fails before creating a tmux session when Linux systemd
  user-scope support is unavailable; it never silently falls back.
- macOS and Windows keep their existing runtime paths.
- Crash recovery and cleanup that remains pending after a failed stop require a
  later lifecycle/persistence change; this ADR intentionally leaves that work
  separate.

## Acceptance evidence

The implementation must include hermetic state/ordering tests and an explicit
Linux canary proving that a `setsid` child is in the expected scope and is gone
after Destroy/Restart, while an unrelated process outside the scope survives.
