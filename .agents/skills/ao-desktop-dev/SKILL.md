---
name: ao-desktop-dev
description: Launch, restart, or troubleshoot the real AO Electron desktop app from this repository; run a checkout against isolated or real local AO data; combine PR branches for local UI review; and diagnose stale Electron processes, port conflicts, or preload bridge mismatches. Use whenever asked to run, open, show, or visually verify AO frontend changes in the actual desktop app rather than ao preview, dev:web, or mock data.
---

# AO Desktop Dev

Run the Electron application from the current checkout and verify it in the native window. Keep the dev daemon, renderer, Electron preload, and selected data mode explicit so the user never reviews a mock or stale build by accident.

## Choose the data mode

Ask only when the request does not make the desired data source clear.

- Use **isolated mode** by default for implementation and destructive testing. Set `ISOLATE_DEV=true` (dev no longer isolates by default — see `frontend/src/shared/daemon-attach.ts`). Electron then uses port `3002`, `~/.ao/dev/running.json`, `~/.ao/dev/data`, and `~/.ao/dev/electron`.
- Use **real-data mode** only when the user explicitly asks to see this machine's actual AO projects or sessions. Start the checkout's dev daemon on the isolated dev port/run file (`ISOLATE_DEV=true`) while pointing `AO_DATA_DIR` at the real AO data directory. This is a separate daemon process using real data; do not describe it as the installed app's daemon.
- Never try to attach an unpackaged Electron app directly to a packaged daemon from another checkout. The supervisor intentionally rejects daemon identity mismatches.
- Warn before actions in real-data mode that create, terminate, rename, or otherwise mutate sessions. Merely opening and inspecting the UI is expected.

## Preflight

1. Work from the repository root and inspect `git status --short --branch`.
2. Read `frontend/package.json`, `frontend/src/main.ts`, and `frontend/forge.config.ts` when the launch behavior may have changed. Treat source as authoritative over this skill.
3. Confirm Node/npm and the Go version required by `backend/go.mod` are available. Run `npm ci` in `frontend/` only when dependencies are absent or inconsistent; do not reinstall on every launch.
4. Check for an existing dev instance from this exact checkout before starting another. Do not kill by process name alone, and never terminate every Electron process.

On macOS/Linux, inspect checkout-scoped processes and listeners with finite commands:

```bash
ps -axo pid=,ppid=,pgid=,lstart=,command= | rg 'frontend/(node_modules/.bin/electron-forge|node_modules/electron/dist/Electron)'
lsof -nP -iTCP:3002 -sTCP:LISTEN
```

Use the full command paths and start times to distinguish this checkout from other AO worktrees. On Windows, use `Get-CimInstance Win32_Process` and `Get-NetTCPConnection`; preserve the same exact-target rule.

## Launch the app

Run Electron Forge in a foreground interactive process so its output and restart input remain available. Do not append `&`, use a detached shell, or start a browser preview instead.

### Isolated mode

On macOS/Linux:

```bash
cd frontend
env -u AO_DATA_DIR -u AO_RUN_FILE -u AO_PORT ISOLATE_DEV=true npm run dev
```

On PowerShell:

```powershell
Set-Location frontend
Remove-Item Env:AO_DATA_DIR, Env:AO_RUN_FILE, Env:AO_PORT -ErrorAction SilentlyContinue
$env:ISOLATE_DEV = "true"
npm run dev
```

### Real-data mode

First resolve and report the actual data directory. In an AO worker session, `AO_DATA_DIR` normally already identifies it. Otherwise the repository default is the absolute path corresponding to `~/.ao/data`.

Keep the real data directory but set `ISOLATE_DEV=true` (with port/run-file overrides removed) so the dev app retains its own daemon handshake and port instead of colliding with an installed app's daemon on the shared default port:

```bash
cd frontend
env -u AO_RUN_FILE -u AO_PORT ISOLATE_DEV=true AO_DATA_DIR="${AO_DATA_DIR:-$HOME/.ao/data}" npm run dev
```

PowerShell equivalent:

```powershell
Set-Location frontend
$realAoData = if ($env:AO_DATA_DIR) { $env:AO_DATA_DIR } else { Join-Path $HOME ".ao/data" }
Remove-Item Env:AO_RUN_FILE, Env:AO_PORT -ErrorAction SilentlyContinue
$env:ISOLATE_DEV = "true"
$env:AO_DATA_DIR = $realAoData
npm run dev
```

Do not print unrelated environment variables: AO sessions may carry credentials. It is safe to report only `AO_DATA_DIR`, `AO_RUN_FILE`, `AO_PORT`, and `ISOLATE_DEV`.

## Confirm the correct app is ready

Wait for all of these signals:

- Electron Forge reports `Launched Electron app`.
- The daemon reports `daemon listening`, normally on `127.0.0.1:3002` in isolated mode (`ISOLATE_DEV=true`) or `127.0.0.1:3001` in real-data/shared mode, unless it explicitly reports another bound port.
- The renderer makes successful requests such as `/api/v1/projects` and `/api/v1/sessions`.
- The native Electron window shows the checkout's UI. A Vite URL opened in a normal browser is not equivalent because it lacks the Electron preload bridge.

If several AO windows exist, identify the newest Electron main process belonging to this checkout. On macOS, foreground that exact PID only when needed:

```bash
osascript -e 'tell application "System Events" to set frontmost of first process whose unix id is <verified-pid> to true'
```

Do not claim real data from appearance alone. Verify the selected data directory and successful API responses. Do not claim visual success from compilation alone; interact with the requested flow in Electron or ask the user for a screenshot when screen capture is unavailable.

## Reload changes correctly

- Renderer-only React/CSS changes normally hot reload.
- Changes to `frontend/src/main.ts`, preload files, Forge configuration, shared bridge types, or IPC registration require an Electron main-process restart. Type `rs` into the active Forge terminal or fully stop and relaunch the dev process.
- A renderer error such as `aoBridge.<method> is not a function` almost always means the renderer hot-reloaded against a stale preload. Restart Electron before changing code to add guards around a bridge method that should exist.
- Backend changes require restarting the managed dev daemon or the Electron process; renderer hot reload cannot apply Go changes.

After a restart, confirm the new Electron PID/start time and re-check the logs for the original error.

## Preview multiple PRs locally

When the user wants to see multiple unmerged PRs together:

1. Require a clean worktree and fetch each PR head.
2. Create a clearly named local-only integration branch using the session/repository branch convention.
3. Merge or cherry-pick the requested PR heads without rewriting or pushing either source branch.
4. Resolve overlaps according to the requested combined behavior and run focused typechecks/tests.
5. Let renderer changes hot reload; restart Electron if either PR changes main/preload/IPC code.
6. State that the integration branch is local-only. Do not push it unless the user explicitly requests a combined PR.

Once one PR merges, prefer rebasing the remaining PR onto current `main`; the normal PR branch then contains both views without a local integration merge.

## Stop without collateral damage

1. Send Ctrl+C to the foreground dev process and wait briefly for Electron and its managed daemon to exit.
2. Re-run the checkout-scoped process query. If the exact process group remains, identify the group created by this launch before sending `TERM` to that group.
3. On Windows, terminate the verified parent tree only. Never use a broad Electron, Node, Go, or port-based kill.
4. Confirm the captured Electron PID and dev daemon listener are gone. Leave installed AO and other worktrees untouched.

## Common mistakes

- `ao preview` controls the AO Browser panel; it does not launch the desktop shell.
- `npm run dev:web` is useful for browser-only renderer work but does not provide Electron APIs or native chrome.
- Renderer URLs can move from `5173` when a port is occupied. Trust Forge's printed URL rather than assuming one.
- Multiple dev instances share `~/.ao/dev/electron`; avoid running them concurrently because Chromium profile state can collide.
- An inherited `AO_DATA_DIR` changes dev mode from isolated data to real data. Always choose and report the mode instead of inheriting it accidentally.
- Dev no longer isolates by default: omitting `ISOLATE_DEV=true` makes `npm run dev` share the installed app's port (3001) and state dir (`~/.ao`) instead of the sandboxed `3002`/`~/.ao/dev` setup. Always set it explicitly for isolated or real-data mode; do not assume isolation from unset `AO_PORT`/`AO_RUN_FILE`/`AO_DATA_DIR` alone.
- Repeated `/healthz/` 404 entries from external probes can be noisy; readiness is determined by Electron's daemon status and successful API traffic, not by log volume.

## Completion report

Report:

- checkout and branch being displayed;
- isolated or real-data mode and the non-sensitive data/run-file paths;
- renderer and daemon addresses actually reported;
- whether Electron was restarted for main/preload changes;
- user flows exercised in the native window;
- any remaining stale process, port, or visual-verification limitation.
