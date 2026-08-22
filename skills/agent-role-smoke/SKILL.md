---
name: agent-role-smoke
description: Run AO agent role smoke tests for orchestrator and worker behavior across installed or selected agent harnesses. Use when validating that Agent Orchestrator can launch agents as orchestrators/workers, that role-specific system prompts are being generated and delivered, and that agents respond correctly to simple UI-edit tasks.
---

# Agent Role Smoke

Use this skill to validate AO agent harnesses in both roles:

- **orchestrator**: launches with orchestrator standing instructions and delegates implementation to a worker.
- **worker**: launches with worker standing instructions and performs the assigned edit in its own workspace.

Do not ask agents to print or reveal their exact system prompt. AO deliberately includes a standing-instruction confidentiality guard. Verify prompt delivery through role behavior and AO-generated prompt artifacts instead.

## Quick Start

Start the AO dev app first:

```bash
cd frontend
npm run dev
```

This starts both the daemon and Electron supervisor. Once the app is running, start testing the selected orchestrator or worker behavior directly. Do not run API generation, lint, typecheck, or broad checks by default; assume those are fine unless the user explicitly asks for them or the dev app/manual test exposes a problem.

From the repo root, in another terminal:

```bash
node skills/agent-role-smoke/scripts/agent-role-smoke.mjs
```

Common focused runs:

```bash
node skills/agent-role-smoke/scripts/agent-role-smoke.mjs --agent codex --role worker
node skills/agent-role-smoke/scripts/agent-role-smoke.mjs --agent codex --role orchestrator
node skills/agent-role-smoke/scripts/agent-role-smoke.mjs --agents codex,claude-code --role both
node skills/agent-role-smoke/scripts/agent-role-smoke.mjs --all-supported
```

By default the script tests installed/authorized harnesses from `ao agent ls --refresh --json`. Pass `--all-supported` only when you intentionally want missing binaries or unauthenticated harnesses recorded as failures.

The script configures the disposable test project with `permissions: "bypass-permissions"` by default for both worker and orchestrator roles. This keeps role smoke tests from stalling on native prompts such as "trust this folder" or command approval dialogs. Use `--manual-permissions --keep` when you intentionally want to inspect and answer native permission prompts in the AO session terminal.

During worker and orchestrator live-behavior waits, the script watches the session's tmux pane. If pane output does not change for 7 seconds, it sends one `Enter` to the attached tmux pane and then waits for another 7 seconds of unchanged output before sending another. This is a generic fallback for permission, confirmation, or continue prompts that do not match the visible prompt heuristics.

Before sending an orchestrator task, the script verifies the orchestrator tmux pane exists, has produced output, and is not sitting on a visible blocking prompt. If a startup or delegation poll shows a permission/approval prompt, the script sends one best-effort `Enter` to the orchestrator pane before continuing. For Cursor runs, it also pre-trusts the predictable orchestrator workspace under `~/.cursor/projects` and answers the Cursor workspace-trust screen with `a` if it still appears.

The orchestrator task includes a unique `AO_ROLE_SMOKE_TASK ...` marker. After `ao send`, the script captures the orchestrator pane and confirms either the marker appears or a new collapsed pasted-text block appears in CLIs that hide pasted content. If neither delivery signal appears, it checks for a visible blocking prompt, sends one `Enter` if needed, and retries the message send before testing whether the orchestrator actually delegates to a worker.

Before creating a new test project, the script removes stale `role-smoke-*` projects and sessions. This prevents an old smoke-test orchestrator, especially one running a different harness, from being reused or confusing a new run. Pass `--no-preflight-clean` only when investigating a preserved smoke fixture.

## Preconditions

1. Start the AO dev app from `frontend/` with `npm run dev`.
2. Wait for the daemon and Electron supervisor to be running.
3. Begin testing the given orchestrator. Do not add separate preflight checks unless something fails or the user asks for deeper validation.

## What the Script Does

- Creates a disposable git fixture with a small UI file.
- Registers the fixture as an AO project.
- For each selected harness:
  - configures the project role override for worker and/or orchestrator, including test-friendly permission bypass unless `--manual-permissions` is set.
  - preflights old `role-smoke-*` sessions and removes stale test orchestrators before starting the new orchestrator.
  - spawns a worker with `ao spawn --harness` for worker tests.
  - spawns an orchestrator through `POST /api/v1/orchestrators` for orchestrator tests.
  - verifies the orchestrator is usable, sends a concrete UI-edit task, and confirms the task marker reached the orchestrator pane.
  - checks generated prompt files under the AO data dir.
  - checks session role/harness metadata and fixture worktree diffs.

## Pass Criteria

- **Worker pass**: session launches with the requested harness, AO generated a non-empty system prompt artifact, and the worker changes the fixture's `src/App.jsx` so the New Task button is green or the notification icon is red.
- **Orchestrator pass**: session launches with the requested harness, AO generated a non-empty orchestrator prompt artifact, and the orchestrator creates or redirects a worker instead of editing its own workspace.
- **Skip**: harness is not installed or is explicitly unauthorized in the AO agent catalog.

The orchestrator task prompt pins the exact `--ao-bin` value and project id. This prevents the orchestrator from trying `ao start`, opening Electron, or delegating through a different AO binary than the smoke harness is using.

If a live harness cannot make model calls because auth/quota/model setup is incomplete, treat that as an environment failure for the harness, not proof that AO role prompting is broken.

## Useful Flags

```bash
--agent <id>              Test one harness.
--agents <a,b,c>          Test a comma-separated harness list.
--role worker|orchestrator|both
--all-supported           Try every AO-supported harness.
--ao-bin <path>           Use a specific ao binary instead of PATH.
--timeout-ms <ms>         Poll timeout for live behavior checks.
--permission-mode <mode>  Permission mode for test sessions; defaults to bypass-permissions.
--manual-permissions      Do not set AO permissions; answer native prompts manually.
--no-preflight-clean      Keep old role-smoke-* projects/sessions before this run.
--stop-ao-after           Run `ao stop` after the smoke test finishes.
--stop-electron-after     Best-effort stop of local AO/Electron desktop processes after the test.
--keep                    Keep sessions, project registration, and fixture dir.
--verbose                 Print command/API details while running.
```

Use `--keep` when investigating a failure so the AO sessions and fixture worktrees remain available for inspection.
