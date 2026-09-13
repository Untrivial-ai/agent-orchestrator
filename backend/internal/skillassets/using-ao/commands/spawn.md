# ao spawn

Spawn a worker agent session in a registered project. The session runs the chosen agent in a fresh git worktree. Register the project first with `ao project add`.

## Syntax

```
ao spawn [flags]
```

## Flags

| Flag | Meaning | Default / Required |
|---|---|---|
| `--branch string` | Branch for the session worktree | `ao/<session-id>/root` |
| `--claim-pr string` | Immediately claim an existing PR for the spawned session | - |
| `--harness string` | Agent harness to use (see list below) | Project `worker.agent`; required if the project has none |
| `--issue string` | Issue id to associate with the session | - |
| `--permission string` | Session permission override: `read-only`, `default`, `accept-edits`, `auto`, `bypass-permissions` | Project/role default |
| `--name string` | Display name shown in the sidebar (max 20 characters) | Required |
| `--no-takeover` | Refuse if another active session owns the claimed PR (requires `--claim-pr`) | - |
| `--project string` | Project id to spawn the session in | Required |
| `--prompt string` | Initial prompt for the agent | - |

`--agent` is an alias for `--harness`.

Available harnesses: `claude-code`, `codex`, `aider`, `opencode`, `grok`, `droid`, `amp`, `agy`, `crush`, `cursor`, `qwen`, `copilot`, `goose`, `auggie`, `continue`, `devin`, `cline`, `kimi`, `kiro`, `kilocode`, `vibe`, `pi`, `autohand`.

## Examples

```bash
# Spawn a worker for issue 142 in the agent-orchestrator project
ao spawn --project agent-orchestrator --issue 142 --name "fix-session-leak" --prompt "Fix the session leak described in issue 142. Branch off upstream/main."
```

```bash
# Spawn a worker and immediately claim an open PR
ao spawn --project agent-orchestrator --name "review-pr-88" --claim-pr 88 --harness claude-code
```

```bash
# Inspect with a preventive filesystem restriction
ao spawn --project agent-orchestrator --agent codex --mode chat --permission read-only --name reader --prompt "Inspect the code and report findings."
```

A worker launched `read-only` keeps that restriction across restarts and settings
changes. Only Codex Chat currently supports it; unsupported harnesses and terminal
mode fail before creating a session. `GET /api/v1/settings` reports supported
`chatPermissionModes` by harness before launch.
