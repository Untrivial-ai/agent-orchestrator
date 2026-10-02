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
cmd --skip-onboarding --no-auto-update --trust [--model <model>] [--effort <level>]
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
- `--effort <level>` is forwarded from the agent config effort field when set.
  `cmd --list-models` advertises no per-model effort levels, so AO forwards the
  raw level without catalog validation (empty keeps Command Code's default);
  unknown values are rejected by `cmd --effort` at launch.

Permission modes map as follows:

| AO permission mode | Command Code flags |
| --- | --- |
| default | none (uses Command Code's configured default) |
| accept-edits | `--permission-mode accept-edits` |
| auto | `--permission-mode accept-edits` |
| bypass-permissions | `--yolo` |

Command Code has no separate "auto" tier, so accept-edits and auto both use
`--permission-mode accept-edits`.

## Activity Tracking

AO merges workspace-local hooks into `.commandcode/settings.json` for
`SessionStart`, `PreToolUse`, `PostToolUse`, and `Stop`. These callbacks report
active/idle activity and persist Command Code's native `session_id`. AO marks
this coverage partial because Command Code does not expose a permission-request
hook, so it cannot reliably distinguish an approval prompt from other in-turn
work.

On `SessionStart`, the same hook returns Command Code's supported
`additionalContext` response with AO's standing instructions. The prompt stays
in AO-owned storage under `~/.ao` and AO does not modify the project's
`AGENTS.md` or `.commandcode/AGENTS.md`.

## Chat Mode

**Supported** since Command Code v1.74.0, which ships a first-party ACP agent.

AO launches `cmd acp` and speaks the Agent Client Protocol over stdio. Verified
by a live handshake against v1.74.0 on 2026-10-02:

```text
protocolVersion 1
loadSession      true                  -> resume works
promptCapabilities{image: true, embeddedContext: true}
mcpCapabilities  {http: true}
sessionCapabilities{list: {}, resume: {}, close: {}}
```

`session/new` advertises five modes and two live config options:

| AO permission mode | Command Code mode id        |
| ------------------ | --------------------------- |
| default            | `default`                   |
| accept-edits       | `auto-accept`               |
| auto               | `auto-accept`               |
| bypass-permissions | `bypass`                    |

The model picker and effort control come from the provider's own catalog
(`model`, with 85 entries at the time of writing, and `effort` in category
`thought_level`), so AO keeps no hardcoded model list. Command Code also
advertises the `plan` and `dont-ask` modes, which have no AO equivalent and are
reachable only through Command Code's own UI.

The Chat binding lives in
`backend/internal/adapters/chatdriver/commandcodeacp/` and is registered in
`chatdriver/registry`, which is what makes the TUI ⇄ Chat toggle appear for this
harness.

## Restore

AO restores a Command Code session with `cmd --resume <session-id>` after the
workspace hook has captured the native session id. Before the first hook is
received, restore falls back to a fresh launch with AO's saved task.

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
