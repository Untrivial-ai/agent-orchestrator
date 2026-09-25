# OpenHands Adapter

OpenHands is integrated into Agent Orchestrator as an interactive Terminal UI
harness. AO launches the user's own `openhands` binary (the
[OpenHands CLI](https://github.com/OpenHands/OpenHands-CLI)) inside the session
worktree.

## Install

Install the OpenHands CLI with `uv` (recommended) or the upstream installer,
then make sure the `openhands` binary is available on `PATH`:

```bash
uv tool install openhands
curl -fsSL https://install.openhands.dev/install.sh | sh
```

The package requires Python 3.12; `uv` resolves and, if needed, downloads it.
AO's Harness settings offer the `uv` install everywhere and the upstream
installer on macOS and Linux.

OpenHands CLI 1.12.0 or newer is required: it is the first release that loads
`.openhands/hooks.json`, which AO needs for session identity and activity. AO
checks the version once per resolved binary when a session launches rather than
during readiness probes, because `openhands --version` starts a Python process
that takes several seconds.

AO resolves `openhands` from:

- `PATH`
- `/usr/local/bin/openhands`, `/opt/homebrew/bin/openhands`
- `~/.local/bin/openhands` (both `uv` and the upstream installer)
- `%USERPROFILE%\.local\bin\openhands.exe` on Windows

## Setup

OpenHands keeps its LLM configuration in `~/.openhands/agent_settings.json`
(or `$OPENHANDS_PERSISTENCE_DIR`). Until a model is configured, the TUI opens
its first-run settings screen instead of working, so AO reports OpenHands as
unauthenticated. Harness settings offer **Set up OpenHands**, which opens that
native settings screen in an AO terminal.

OpenHands has no `--model` flag. When the agent config sets a model, AO
launches with `env LLM_MODEL=<model> openhands --override-with-envs`;
OpenHands layers that over the saved settings for the session only and never
writes it back. Model names use LiteLLM form (for example
`anthropic/claude-sonnet-4-5`). `--override-with-envs` also applies any
`LLM_API_KEY` or `LLM_BASE_URL` already present in the session environment.

## Supported AO Mode

OpenHands is exposed through AO's Terminal UI mode. A fresh prompted session
launches as:

```bash
[env LLM_MODEL=<model>] openhands [--override-with-envs] [--llm-approve|--always-approve] --task=<prompt>
```

`--task` queues the prompt into the interactive TUI, so the terminal stays
input-ready after the task finishes. A restored session resumes the native
conversation reported by hooks:

```bash
openhands [--llm-approve|--always-approve] --resume <conversation-id>
```

AO permission modes map onto OpenHands confirmation policies:

| AO mode | OpenHands |
| --- | --- |
| default | always confirm (no flag) |
| accept-edits | always confirm: OpenHands has no edits-only policy, and its risk analyzer would also auto-run low-risk shell commands |
| auto | `--llm-approve`: an LLM analyzer confirms only predicted high-risk actions |
| bypass | `--always-approve` |

Tool allow/deny lists are not supported; launches that request them fail
rather than run unrestricted.

## Instructions

OpenHands has no system-prompt flag and ignores `SessionStart` hook output. AO
delivers its role/system instructions through the `UserPromptSubmit` hook,
whose top-level `additionalContext` OpenHands appends to each user message.
Repeating them per message keeps them present after OpenHands condenses older
history.

## Activity Tracking

AO installs managed hooks in `.openhands/hooks.json` inside the session
worktree:

- `SessionStart` reports `active` and the native conversation ID
- `UserPromptSubmit` reports `active` and injects AO instructions
- `Stop` reports `idle`

OpenHands creates its conversation, and fires `SessionStart`, when the first
message is processed rather than at TUI startup. OpenHands has no
permission-request hook; confirmation prompts happen inside the TUI.

OpenHands reads either `{"hooks": {"PascalCase": [...]}}` or a direct
`{"snake_case": [...]}` file, and silently drops top-level event keys when a
`hooks` wrapper is present. Before installing, AO rewrites an existing
direct-format or mixed-spelling file into the wrapped PascalCase form so the
user's own hooks keep firing next to AO's.

OpenHands reads `~/.openhands/hooks.json` only when the workspace has no
`.openhands/hooks.json`. Because AO creates the workspace file, user-global
OpenHands hooks do not run in AO worktrees.

Hook delivery is best-effort. A missing AO executable, an unavailable daemon,
or a hook timeout never interrupts the OpenHands session.

## Chat Mode

Not supported yet. `openhands acp` exists and is a candidate for a follow-up
native ACP Chat driver.
