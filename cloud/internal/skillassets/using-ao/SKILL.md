---
name: using-ao
description: "Catalog of the AO Cloud `ao` CLI available inside a cloud sandbox: session orchestration, pull request attachment, and control of the session browser."
trigger: "Using the ao CLI inside an AO Cloud sandbox for orchestration, pull requests, or browser navigation and interaction."
---

# AO Cloud CLI Catalog

`ao` inside a cloud sandbox combines control-plane commands with a client for
the session-local browser service. It is a different, smaller CLI than the
desktop `ao`: there is no `ao session`,
`ao project`, `ao status`, or `ao preview` here. The session-local `ao browser`
command is available. Run `ao help` for the authoritative command list.

| Command | What it does | Who can run it | Details |
|---|---|---|---|
| `spawn` | Create a worker session in its own fresh sandbox | Orchestrators only | [commands/orchestration.md](commands/orchestration.md) |
| `list` (`ls`, `status`) | List the workers this orchestrator spawned, with branch, status, and PR facts | Orchestrators only | [commands/orchestration.md](commands/orchestration.md) |
| `send` | Send a message into a worker's conversation | Orchestrators only | [commands/orchestration.md](commands/orchestration.md) |
| `kill` (`delete`, `rm`) | Terminate a worker session and its sandbox | Orchestrators only | [commands/orchestration.md](commands/orchestration.md) |
| `report` | Send a message to the orchestrator that spawned this session | Workers with an orchestrator parent | [commands/orchestration.md](commands/orchestration.md) |
| `claim-pr` | Attach an existing pull request to this session | Any session | [commands/pull-requests.md](commands/pull-requests.md) |
| `browser` | Inspect and control this session's shared browser | Any session with the browser service | [commands/browser.md](commands/browser.md) |

## Conventions

- `ao list --json` prints machine-readable output; `--all` includes terminated
  workers (hidden by default).
- `--agent` is an alias for `--harness` on `ao spawn`.
- Session ids are UUIDs shown by `ao list`.
- Every session runs in its own isolated sandbox. Never try to reach another
  session's sandbox directly (no ssh, no shared filesystem); the orchestration
  commands above are the only channel between sessions.
- For browser navigation or interaction, read [commands/browser.md](commands/browser.md)
  and use `ao browser`. Do not claim browser access is unavailable before
  checking `ao browser status`.
- A `send` to a worker whose sandbox is still provisioning is queued by the
  control plane and delivered when the worker's agent is ready — check
  `runtimeConnected` in `ao list --json` to see whether a worker is live.
- Spawning and messaging are idempotent server-side; a command that prints a
  success line has been durably accepted even if the effect is not visible yet.
- Pull requests and reviews are handled through socket helpers described in
  [commands/pull-requests.md](commands/pull-requests.md), not through `ao`.
