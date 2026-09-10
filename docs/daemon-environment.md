# Daemon environment: the GUI-launch PATH/credentials problem

Status: proposed
Scope: desktop (Electron) launch of the AO daemon on macOS (and any GUI-launched
desktop platform)

## Summary

When the desktop app is launched from Finder/Dock/Spotlight, the daemon it spawns
inherits a stunted environment (minimal `PATH`, no shell-exported credentials).
The daemon then cannot find `git`/the agent CLIs, and the agents it launches
cannot see API keys. Packaged macOS/Linux builds carry their own tmux and do
not depend on a machine installation; development and standalone daemon runs
still resolve tmux from `PATH`. The same app launched from a terminal works,
because a terminal-started process inherits the shell's fully-populated
environment. The fix is to resolve the user's login-shell environment once at
startup and use it as the base for the daemon's environment.

## Problem statement

The Electron supervisor spawns the Go daemon with the environment it forwards in
`daemonEnv()` (`frontend/src/main.ts`), which is essentially `...process.env`
plus AO's telemetry defaults. The daemon, in turn, is the parent of every agent
session (it execs `tmux`, which runs `claude`/`codex`, etc.), and the agent's
`PATH` is derived from the daemon's own `PATH`
(`runtimeEnv` -> `HookPATH(m.executable, os.Getenv, ...)` in
`backend/internal/session_manager/manager.go`).

AO keeps an AO-only directory first after adding agent and Node runtime
directories, including Codex Chat provider launches, and applies the same pin
to reviewer and shell-terminal launches.
Interactive login shells can still reorder `PATH` through user startup files,
so this is a launch invariant and a best-effort convenience inside a shell.
When the install directory contains other executable tools, AO creates an
`ao` shim under its resolved absolute data directory instead of promoting those
sibling tools. Windows provides both command-shell and Git Bash compatible
entries in that isolated directory.

So whatever environment the daemon receives propagates to the entire stack:

```
launchd (or terminal) -> Electron main -> daemon -> tmux -> agent (claude/codex)
```

When that environment is impoverished, everything downstream breaks.

### Observed symptoms

All of these were traced to the same root cause:

- Terminal pane stuck on "Terminal disconnected - reattaching...".
- Terminal pane showing "Terminal ended ... but the session is not marked
  terminated yet."
- Sessions stuck `idle` + `is_terminated = 0` in the store, never reaped, and
  therefore not restorable (`Restore` requires `IsTerminated`, otherwise
  `ErrNotRestorable`).
- `tmux list-sessions` showing sessions as alive-but-unreachable or dead,
  depending on which socket universe was inspected.

The unifying cause: the running, GUI-launched daemon cannot execute
`/opt/homebrew/bin/tmux` (and friends), so its liveness probes error
(`ProbeFailed`, never `ProbeDead`, so the reaper never terminates the row) and
its terminal attaches cannot spawn `tmux attach`.

## Root cause: GUI apps do not inherit the shell environment

On macOS, a process's environment is inherited solely from its parent. The
parent differs by launch method:

- **Terminal launch.** The terminal starts a login/interactive shell
  (`zsh -l`). That shell sources `/etc/zprofile`, `~/.zprofile`, `~/.zshrc`,
  etc. Those files are the only thing that sets the rich environment:
  `eval "$(/opt/homebrew/bin/brew shellenv)"` adds `/opt/homebrew/bin` to
  `PATH`; `export ANTHROPIC_API_KEY=...` exports credentials. Every process
  started from that terminal inherits the result. The app works.

- **Finder/Dock/Spotlight launch.** The app is started by **launchd**, not by a
  shell. launchd hands the process a fixed, minimal environment
  (`PATH=/usr/bin:/bin:/usr/sbin:/sbin`, `HOME`, `USER`, `TMPDIR`, little else).
  No shell runs anywhere in the chain, so no rc/profile file is ever sourced.
  The homebrew `PATH` and the exported credentials simply do not exist for the
  app, and `daemonEnv()` faithfully forwards that minimal env down to the daemon.

This is deliberate on Apple's part: GUI apps are decoupled from interactive shell
configuration on purpose (it can be slow, interactive, or machine-specific). The
old `~/.MacOSX/environment.plist` escape hatch was removed years ago. This is the
single most common macOS-Electron footgun; it is why packages like `fix-path` and
`shell-env` exist.

### Why "just forward env" is correct in principle

Forwarding the environment is not the bug. The daemon and agents genuinely need:

- `PATH` to resolve `git`, `node`, and the agent CLIs (plus tmux in development
  and standalone daemon runs);
- `HOME` for config/credentials (`~/.gitconfig`, `~/.claude`, `~/.codex`, ssh
  keys);
- shell-exported credentials (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GH_TOKEN`,
  ...);
- locale/proxy (`LANG`, `LC_*`, `HTTPS_PROXY`);
- AO's own vars (telemetry, `AO_DATA_DIR`, `AO_RUN_FILE`, session ids).

The bug is the _source_ of what we forward: under a GUI launch, `process.env` is
launchd's minimal env, not the shell's. The fix is to forward a _good_ base env,
not to stop forwarding.

## Proposed solution: resolve the login-shell environment

Do not reconstruct the shell environment by hand. Run the user's login shell
once, ask it to print its environment, and adopt that as the base for
`daemonEnv()`.

### The mechanism

```
zsh -ilc 'env -0'
```

- `-l` (login): source `/etc/zprofile` and `~/.zprofile` (where the homebrew
  `PATH` line typically lives).
- `-i` (interactive): source `~/.zshrc` (where most `export` lines live).
- `-c 'env -0'`: run one command and exit. `env` dumps the environment the shell
  built after sourcing all config; `-0` separates entries with NUL bytes instead
  of newlines, so values containing newlines parse unambiguously.

The output is a faithful snapshot of "what a terminal would see." Parse it back
into key/value pairs and merge it under the existing forwarded env so explicit
overrides still win:

```
finalEnv = { ...shellEnv, ...process.env, AO_*: defaults }
```

### Worked example

GUI-launched daemon env (before):

```
PATH=/usr/bin:/bin:/usr/sbin:/sbin
HOME=/Users/<user>
```

After `zsh -ilc 'env -0'` resolution:

```
PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:/usr/sbin:/sbin
HOME=/Users/<user>
ANTHROPIC_API_KEY=sk-ant-...
GH_TOKEN=ghp_...
LANG=en_US.UTF-8
```

The daemon can now resolve `/opt/homebrew/bin/tmux`, and agents inherit the
credentials.

### Implementation details

Place the resolution in Electron's `daemonEnv()` (`frontend/src/main.ts`), the
parent that hands env to the daemon.

- **Resolve once, cache.** Sourcing rc files can take 100ms to >1s
  (nvm/pyenv/...). Do it a single time at startup; never per-session.
- **Pick the shell robustly.** Prefer `process.env.SHELL`; under launchd it may
  be absent, so fall back to the user record
  (`dscl . -read /Users/$USER UserShell`), then `/bin/zsh`. Do not hardcode zsh;
  honor bash/fish.
- **Isolate the payload.** Interactive shells can print banners/motd/prompts to
  stdout. Bracket the real output with a sentinel and read only after it:
  `zsh -ilc 'echo __AO_ENV_START__; env -0'`.
- **No stdin, with a timeout.** Run with `</dev/null` and a ~2-3s timeout so a
  misconfigured rc that waits for input cannot hang startup.
- **Fallback on any failure.** If the probe fails, times out, or exits nonzero,
  fall back to a static base: prepend
  `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin` and pull
  through known credential vars. A weird shell config then degrades to "tmux
  and git resolve" rather than "broken."

### Platform scope

- macOS: required (this is where the GUI/launchd split bites).
- Linux: the same class of problem exists for `.desktop`-launched apps; the same
  resolution applies.
- Windows: not applicable in the same form; a static `PATH` floor is sufficient.

This matches what `shell-env`/`fix-path` do; the logic above is the entirety of
it. We shell out once to the user's own shell and adopt its result.

## Testing

- Parser unit test: feed NUL-separated output, including a value containing a
  newline and leading banner noise before the sentinel; assert the resulting map
  is correct and the noise is dropped.
- Fallback test: simulate probe failure/timeout; assert the static PATH floor and
  credential pass-through are applied.
- Manual: launch the packaged app from Finder (not a terminal) on a machine
  without tmux on `PATH`; confirm a new session spawns and attaches through the
  bundled `resources/tmux/bin/tmux`, while `git`/agent binaries still resolve.

## Relevant code

- `frontend/src/main.ts` - `daemonEnv()` (env forwarded to the daemon), daemon
  spawn.
- `backend/internal/session_manager/manager.go` - `runtimeEnv` / `HookPATH`
  (agent `PATH` derived from the daemon's `PATH`); `spawnEnv`.
- `frontend/scripts/build-tmux.mjs` - pinned, checksum-verified static dependency
  build copied into the macOS/Linux package.
- `frontend/src/shared/bundled-tmux.ts` and `frontend/src/main.ts` - packaged
  resource resolution, durable versioned staging under the AO data directory,
  and `AO_TMUX_BINARY`/AO-owned socket injection.
- `backend/internal/adapters/runtime/tmux/tmux.go` - honors
  `AO_TMUX_BINARY`; standalone runs fall back to `exec.LookPath("tmux")`.
- `backend/internal/observe/reaper/reaper.go`,
  `backend/internal/lifecycle/runtime.go` - liveness -> termination
  (`ProbeFailed` never terminates, so a daemon that cannot run `tmux` strands
  sessions).
