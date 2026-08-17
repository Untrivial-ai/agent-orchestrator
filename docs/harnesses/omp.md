# OMP Adapter

OMP is integrated into Agent Orchestrator as an interactive Terminal UI
harness. AO launches the `omp` binary inside the session worktree and streams
the TUI through the existing terminal runtime.

## Install

Install OMP using one of its supported upstream installers, then make sure the
`omp` binary is available on `PATH`.

Common install paths include:

```bash
curl -fsSL https://omp.sh/install | sh
brew install can1357/tap/omp
bun install -g @oh-my-pi/pi-coding-agent
```

Verify the install:

```bash
omp --version
```

AO resolves `omp` from:

- `PATH`
- `/usr/local/bin/omp`, `/opt/homebrew/bin/omp`
- Node-managed global bin directories
- `~/.omp/bin/omp`
- `%AppData%\npm\omp.cmd` and `%AppData%\npm\omp.exe` on Windows

## Supported AO Mode

OMP is exposed through AO's Terminal UI mode. A fresh prompted session launches
as:

```bash
omp "<prompt>"
```

When configured, AO forwards:

- `--model <model>` from the agent config model field
- `--append-system-prompt <text>` for AO's role/system prompt

The process remains interactive after the initial prompt, so users can keep
working directly in the OMP TUI.

## Restore

When AO has captured an OMP native session id in session metadata, restore uses:

```bash
omp --resume <native-session-id>
```

If no native session id is available, AO falls back to a fresh interactive
launch.

## Auth

AO checks OMP auth using local-only signals:

1. `PI_CODING_AGENT_DIR/auth.json`, when `PI_CODING_AGENT_DIR` is set.
2. `~/.omp/agent/auth.json`, when present.
3. Cheap CLI auth/status probes, such as `omp auth status`.

These probes are advisory. A later model call can still fail because of quota,
provider configuration, or selected model availability.

## Not Supported In This Adapter

- ACP editor integration (`omp acp`)
- RPC mode (`omp --mode rpc`)
- Chat UI handoff
- AO-managed OMP extensions or hooks

Those surfaces would require a separate structured protocol driver rather than
the terminal harness adapter.
