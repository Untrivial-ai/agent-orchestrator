# DeepSeek Harness

AO supports [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`)
through two of the profiles it ships: the automation-only ACP server that carries
Chat, and the one-shot headless profile that carries a Terminal UI task.

Install it with npm from Settings → Agents, or manually:

```bash
npm install -g @deepseek-ai/dsh
```

Choose `deepseek-harness` as a project's agent or pass `ao spawn --harness deepseek`.
The harness id is `deepseek-harness`; the binary AO resolves is `dsh`.

## Chat

Chat is the complete path. AO launches `dsh --profile acp` and reads the session's
own configuration catalog, so the model picker offers exactly the models that
session accepts, and the reasoning-effort selector offers the levels that model
advertises (`off`, `low`, `high`, `max`).

Model values are opaque: DeepSeek Harness encodes each choice as a JSON array
string, for example `["deepseek-official","deepseek-v4-flash"]`. AO passes the
value through unchanged, and the project model field accepts the same string.

AO's own effort vocabulary is mapped onto the four Harness levels before it is
sent — `none`/`minimal`/`off` become `off`, `low` stays `low`, `medium`/`high`
become `high`, and `xhigh`/`max` become `max` — so an effort configured for
another harness does not fail a Harness session.

## Terminal UI

AO runs one headless task per terminal session:

```bash
dsh --profile headless "<task>"
```

The profile answers one task, prints the answer, and exits, so a terminal session
is a one-shot run rather than an interactive TUI. The headless profile has no
model or reasoning-effort flag: it uses the model route configured in the
Harness profile, and the AO model field applies to Chat only. The field
description says so.

`dsh --profile headless -` reads the task from stdin, which a terminal launch
never supplies, so AO rejects that exact task text instead of blocking.

## Credentials

DeepSeek Harness owns its credentials, which live in its own store
(`~/.dsh/.credentials.yaml`, or `$DSH_HOME`). It exposes no authentication
surface: the ACP server advertises no auth methods and its `authenticate` call
always succeeds. AO therefore documents setup rather than driving a login, and
reads the store only to report whether a DeepSeek credential is present — the
value is never copied, logged, or forwarded.

## Permissions

Harness has no ACP session modes. Approvals arrive as `session/request_permission`
with one-shot choices, and AO answers them: `auto` answers every request, and
`accept-edits` answers edit, delete, and move tools. Every other mode leaves the
decision to Harness. Nothing is granted up front, so policy Harness itself
enforces stays authoritative.

## Activity and resume

Session activity reaches AO through the ACP stream. DeepSeek Harness has no
workspace hook file for AO to merge into, so AO installs no hooks and reports no
terminal-session activity signals for this harness.

Chat sessions resume through the ACP `session/resume` call. A terminal session
can only resume when AO already holds a native session id, which without a hook
surface is normally not the case, so terminal resume falls back to a fresh run.

## Current limits

- Workspace projects that expose several repositories are not supported over ACP:
  Harness rejects additional directories, and AO reports that rather than
  launching a session with the wrong working set.
- The model catalog is read from a session, and a session needs a working
  directory. Project-free discovery uses a private AO-owned directory under the
  configured data root so the catalog remains available before a project exists.
- AO standing instructions have no Harness surface in terminal mode. The headless
  profile takes no system-prompt flag and has no hook file, so AO does not pass
  the prompt file as a task argument that would be executed as work.
