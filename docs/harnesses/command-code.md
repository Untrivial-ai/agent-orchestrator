# Command Code Adapter

Command Code is integrated into Agent Orchestrator as an interactive Terminal UI
harness. AO launches the user's own `cmd` binary inside the session worktree and
leaves the session open for further work.

## Install

Install Command Code with its upstream npm package, then make sure the `cmd`
binary is available on `PATH`:

```bash
npm install -g command-code
```

Verify the install:

```bash
cmd --version
```

AO's one-click installer uses the same `command-code` npm package. AO resolves
the binary from:

- `PATH`: `cmd`, then `command-code`
- `/usr/local/bin/cmd`, `/opt/homebrew/bin/cmd`
- `/usr/local/bin/command-code`, `/opt/homebrew/bin/command-code`
- Node-managed global bin directories
- `~/.commandcode/bin/cmd`
- `cmdc.cmd`, `cmdc.exe`, and `command-code.cmd` on Windows

On Windows the binary is `cmdc` or `command-code`: `cmd` is reserved for the
built-in command shell.

## Supported AO Mode

Command Code is exposed through AO's Terminal UI mode. A fresh session launches
as:

```bash
cmd --skip-onboarding --no-auto-update --trust [--model <model>]
```

AO then injects the worker's initial task through the terminal once the
interactive UI is ready. Command Code accepts a positional initial message
(`cmd "fix login redirect"`), but injecting after startup means a prompt that
begins with `-` is never parsed as a flag.

- `--skip-onboarding` skips taste onboarding for automated runs.
- `--no-auto-update` disables background self-updates for the AO-managed session.
- `--trust` skips the initial project-trust prompt, which would otherwise block
  every fresh AO worktree.
- `--model <model>` is forwarded from the agent config model field when set.

Permission modes map as follows:

| AO permission mode | Command Code flags |
| --- | --- |
| default | none (uses Command Code's configured default) |
| accept-edits | `--permission-mode auto-accept` |
| auto | `--permission-mode auto-accept` |
| bypass-permissions | `--yolo` |

Command Code has no separate "auto" tier, so accept-edits and auto both use
`--permission-mode auto-accept`.

## Activity Tracking

Not installed yet. Command Code exposes project hooks in
`.commandcode/settings.json` (`SessionStart`, `PreToolUse`, `PostToolUse`,
`Stop`) whose payloads carry the native `session_id`, so a later change can
report lifecycle events and capture the native session id the way the other
hook-capable harnesses do.

## Chat Mode

Not supported. Command Code has no ACP or app-server transport, so it is
Terminal UI-only and is not advertised as a Chat harness.

## Restore

AO restores a Command Code session by launching a fresh session with AO's saved
system prompt and task. Because AO installs no Command Code hooks yet, no native
session id is captured, so the session manager always takes the fresh-launch
fallback. `cmd -r`, `cmd -c`, and `cmd --session` remain available to the user
inside the terminal.

## Auth

AO probes authentication with:

```bash
cmd status
```

Command Code prints `Authentication verified ...` when signed in; AO treats an
explicit "verified" result as authorized and otherwise classifies the output
with the shared authprobe needles. The result is advisory and a later model call
can still fail because of quota, plan, or provider configuration.

Log in with `cmd login`, which opens a browser; an API key can also be pasted in
the terminal.

## Not Supported

- Headless `-p`/`--print` sessions. AO runs supervised, multi-turn Terminal UI
  sessions and does not use Command Code's single-turn print mode.
- Native session resume from AO metadata, until hook-based id capture lands.
- AO system-prompt injection. Command Code exposes no system-prompt flag, and its
  `AGENTS.md` memory tiers are project-owned (a root `AGENTS.md` takes precedence
  over `.commandcode/AGENTS.md`), so AO does not write into them.
- `--effort` wiring. AO's agent config has no effort field to map.
