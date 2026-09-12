# Qoder CLI Adapter

Qoder CLI is integrated into Agent Orchestrator as an interactive Terminal UI
harness, a reviewer, and a structured Chat harness. AO launches the user's own
`qodercli` binary inside the session worktree.

## Install

Install Qoder CLI with either supported method, then make sure `qodercli` is on
`PATH`:

```bash
npm install -g @qoder-ai/qodercli
# or the official installer
curl -fsSL https://qoder.com/install | bash
```

Verify the install and the login:

```bash
qodercli --version
qodercli status -o json   # {"logged_in":true,...}
```

Sign in with `qodercli login`, or export `QODER_PERSONAL_ACCESS_TOKEN`
(`QODER_SERVICE_ACCOUNT_KEY` / `QODER_JOB_TOKEN` also work).

AO drives the `qodercli` binary. `qoder` is a dispatcher script that forwards
some invocations to the IDE, so AO never resolves it.

Chat mode additionally requires **Qoder CLI 1.1.37 or newer** — the release that
introduced the ACP session-mode vocabulary AO maps onto. Older builds are
refused before a chat session starts, with the terminal harness unaffected.

## How AO launches it

```
qodercli --session-id <uuid> [--permission-mode …] [--allowed-tools …]
         [--model …] [--append-system-prompt-file …] --settings <path> [-i <task>]
```

- **The task rides on `-i`**, which keeps the TUI interactive. A bare positional
  prompt is rejected when it looks like a subcommand name.
- **`--settings` carries one AO-owned file** holding three things: trust for the
  session worktree, AO's activity hooks, and `general.enableAutoUpdate: false`.
  The file lives under AO's data dir, and nothing is written into the checkout.
- **Resume** uses `--resume <uuid>`. A new turn is typed into the pane rather
  than passed on the command line, because Qoder CLI resolves `--resume`
  asynchronously inside the TUI.

Permission modes map as `acceptEdits → accept_edits`, `auto → auto`,
`bypassPermissions → bypass_permissions`; AO's `default` passes no flag, leaving
whatever the user configured.

## Trust

Qoder CLI treats an unfamiliar directory as untrusted, and an untrusted
directory silently loses the permission mode AO asked for, drops AO's hooks, and
opens a blocking dialog. AO therefore trusts each session's worktree through the
same `--settings` file that carries the hooks.

One consequence is worth knowing: a repository's own committed
`.qoder/settings.json` is **not** loaded in AO sessions. Qoder CLI decides
whether to read workspace settings from the user's settings file alone, before
AO's flag is merged. AO sessions honor your user settings, AO's own flag
settings, and `AGENTS.md`; project-committed Qoder CLI settings are ignored. Add
the worktree root to `permissions.trustDirectories` in `~/.qoder/settings.json`
if you need them.

## Activity tracking

AO installs Claude-Code-shaped hooks (`SessionStart`, `UserPromptSubmit`, the
tool-use trio, `PermissionRequest`, `Stop`, `Notification`, `SessionEnd`) that
call `ao hooks qodercli <event>`. Qoder CLI's event names, payload fields,
notification types and session-end reasons match Claude Code's, so AO reuses
Claude's interpretation, plus one addition: an elicitation dialog (Qoder CLI's
`AskUserQuestion` form) reports **waiting for input** rather than looking idle.

`SubagentStop` is deliberately not installed — it carries no activity signal,
and its payload would overwrite the session summary with a subagent's last line.

Two environment variables change hook behavior and are worth avoiding in a
daemon environment:

- `CI=true` or `GITHUB_ACTIONS=true` force Qoder CLI into headless mode, which
  is incompatible with a TUI session.
- `GITHUB_SHA` or `SURFACE=Github` switch on strict environment sanitization,
  which strips AO's variables from hook processes. Activity reporting goes quiet
  until they are unset.

## Reviewer

The reviewer launches with `--permission-mode dont_ask` plus `--tools Read Grep
Glob Bash`, so tools outside that set do not exist in the process, allowlisted
read-only commands run without prompting, and anything else is refused rather
than parking an unattended pane on a dialog.

## Chat

Chat runs `qodercli --acp` over pipes. Approval modes can be changed mid-session
(`acceptEdits`, `auto`, `yolo`), and the model is selected from the catalog the
agent advertises. ACP session ids are the same ids the terminal resumes, so a
session can move between Chat and Terminal.

Two current limits: `AskUserQuestion` is unavailable in chat (AO does not
declare the elicitation capability Qoder CLI requires for it), and AO does not
declare history replay for this harness, because Qoder CLI replays a loaded
session's history after answering the load request.
