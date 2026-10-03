# ao spawn

Spawn a worker or orchestrator agent session in a registered project, or a standalone worker session that is not tied to a project.
Standalone sessions run in an AO-managed directory. Register a project first with `ao project add` for project-scoped sessions.

## Syntax

```
ao spawn [flags]
```

## Flags

| Flag | Meaning | Default / Required |
|---|---|---|
| `--branch string` | Branch for the session worktree | `ao/<session-id>/root` |
| `--claim-pr string` | Immediately claim an existing PR for the spawned session | - |
| `--kind string` | Session role: `worker` or `orchestrator` | `worker` |
| `--harness string` | Agent harness to use (see list below) | Project `worker.agent`; required if the project has none |
| `--issue string` | Issue id to associate with the session | - |
| `--model string` | Agent model override for this session only; overrides project/role config | - |
| `--name string` | Display name shown in the sidebar (max 100 characters) | Required |
| `--mode chat\|tui` | Initial session interface; Chat requires harness support | Daemon default, otherwise Terminal UI |
| `--no-takeover` | Refuse if another active session owns the claimed PR (requires `--claim-pr`) | - |
| `--project string` | Project id to spawn the session in | Optional when `--standalone` is used; defaults to `AO_PROJECT_ID` or the current repo's registered project |
| `--standalone` | Spawn a projectless worker session in an AO-managed directory | Disabled when `--project` is set |
| `--prompt string` | Initial prompt for the agent | - |
| `--skip-agent-check` | Skip CLI readiness warnings; the daemon still validates launch readiness | - |
| `--tracker-provider string` | Issue tracker provider: `github` or `gitlab` | `github` |

`--agent` is an alias for `--harness`.

Available harnesses: `claude-code`, `codex`, `aider`, `opencode`, `opencode-v2`, `grok`, `droid`, `amp`, `agy`, `crush`, `cursor`, `qwen`, `gemini`, `copilot`, `goose`, `auggie`, `continue`, `devin`, `cline`, `kimi`, `muse`, `kiro`, `kilocode`, `vibe`, `pi`, `kimchi`, `prime-agent`, `autohand`, `omp`, `fx`, `unreal-agent`, `mimo-code`, `deepseek-harness`. Check `ao agent ls --refresh` for readiness on the installed build. `unreal-agent` is Chat-only; Gemini and MiMo Code are Terminal UI-only.

`fx` is experimental and Terminal UI only: spawn it with `--agent fx --mode tui`.

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
# Spawn an orchestrator using the project's orchestrator configuration
ao spawn --project agent-orchestrator --kind orchestrator --name "coordinate-fix" \
  --prompt "Coordinate the fix and report the worker PR."
```

```bash
# Associate a GitLab issue with a worker
ao spawn --project my-gitlab-app --issue 42 --tracker-provider gitlab \
  --name "fix-issue-42" --prompt "Fix GitLab issue 42."
```
