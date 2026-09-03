---
name: ao-agent-e2e
description: Run and diagnose live Agent Orchestrator end-to-end tests with real orchestrator, worker, and reviewer agents. Use when verifying system-prompt delivery, task delegation, worker placement/activity, Kanban alignment, PR handoff, or reviewer-agent behavior.
---

# AO real-agent E2E

Use this skill only with a real supplied harness. The canonical task is:

> Change the background color of the notification icon to red. Implement the change, run the relevant checks, open a PR, and wait for review.

The runner uses the AO CLI plus the public loopback daemon API exposed by `ao status --json`, and keeps a JSON evidence trail. It does not use fake agents, direct SQLite reads, or internal Go packages.

## Live run

Before spawning anything, state the project, orchestrator/worker/reviewer harnesses, timeout, report path, and cleanup policy. Verify the binary is this repository's AO build and that `ao status` reports the expected daemon endpoint (the local rewrite uses port 3001).

From the repository root:

```bash
node .agents/skills/ao-agent-e2e/scripts/run_agent_e2e.mjs \
  --project agent-orchestrator \
  --harness codex \
  --orchestrator-harness codex \
  --reviewer-harness claude-code \
  --mode tui \
  --report /tmp/ao-agent-e2e.json
```

The default mode is `tui`, which works with installed native harnesses. Use `--mode chat` only when the AO ACP runtime is installed. Use `--ao /tmp/ao` or `AO_BIN` when a bare `ao` may resolve to another install. Use `--task` for a different brief. Use `--cleanup` only when it is safe to terminate sessions created by this run; it never force-deletes dirty worktrees.

The runner exits 0 only when every stage passes. Exit 1 means an observable assertion failed or a required fact was `unobservable`. Exit 2 means configuration or preflight could not start the test. Use `--allow-unobservable` only to produce a diagnostic baseline against a CLI/API that cannot yet expose every fact; do not treat that run as acceptance.

## Harness readiness

When `roles` is selected, the runner performs `ao agent ls --refresh --json` before it creates an orchestrator or worker. This runs AO's fresh, harness-specific local install and authentication probes, including each adapter's supported local configuration and credential sources. The runner never reads or reports credential values.

The report contains `roleReadiness.harnesses` before session creation. Each entry identifies the affected roles and records whether the harness is supported, installed, and `authorized`, `unauthorized`, or `unknown`, along with the safe reason:

- `authorized`: AO's fresh local probe confirmed authorization; role testing continues.
- `unauthorized`: AO found missing or invalid local credentials; authenticate the harness before retrying.
- `unknown`: AO checked the harness's supported local sources but could not prove authorization; the role test stops rather than starting an agent that may block on login.
- not installed or unsupported: the report names the missing CLI or unsupported harness; no agent is started.

This readiness gate is strict and is not bypassed by `--allow-unobservable`. Use the recorded `probe` command and reason when fixing a local harness setup, then rerun the same E2E command.

## Modular runs

Use `--stages all` for the full flow. Use a comma-separated list to run parts independently:

```bash
node .agents/skills/ao-agent-e2e/scripts/run_agent_e2e.mjs \
  --project agent-orchestrator \
  --harness codex \
  --stages preflight,roles
```

Supported stages are `preflight`, `roles`, `kanban-activity`, `reviewer-testing`, and `lifecycle`. The default `all` expands to `preflight,roles,kanban-activity,reviewer-testing`; it intentionally excludes `lifecycle` because kill/restore mutates a live session. Older stage names are accepted as aliases: `orchestrator` and `delegation` map to `roles`, `work-pr` maps to `kanban-activity`, `reviewer` maps to `reviewer-testing`, and `kill-restore` maps to `lifecycle`.

To resume or test later stages against existing sessions:

```bash
node .agents/skills/ao-agent-e2e/scripts/run_agent_e2e.mjs \
  --project agent-orchestrator \
  --harness codex \
  --stages kanban-activity,reviewer-testing \
  --worker-session <worker-session-id>
```

Use `--orchestrator-session`, `--worker-session`, and `--reviewer-session` to inspect known sessions instead of requiring the runner to create every session in the same process.

To test session kill/restore explicitly:

```bash
node .agents/skills/ao-agent-e2e/scripts/run_agent_e2e.mjs \
  --project agent-orchestrator \
  --harness codex \
  --stages lifecycle \
  --lifecycle-session <session-id>
```

Use `--lifecycle-session` for the exact target. If omitted, lifecycle falls back to `--worker-session`, then a worker created by the `roles` stage, then `--orchestrator-session`.

## What the runner checks

- Preflight: AO binary, daemon, project, and harness configuration.
- Roles: fresh per-harness installation/authentication readiness, then real orchestrator spawn, system-prompt byte evidence, prompt artifact evidence, task visibility, worker delegation, worker role prompt marker, and live worker session state.
- Kanban activity: worker activity/status changes are sampled, branch and PR facts are detected from API session detail when available, and missing worktree/tracker/PR facts are recorded as non-passing observation gaps.
- Reviewer testing: review records are polled until a completed reviewer run with session, status, and verdict evidence is visible.
- Lifecycle: a selected session is killed, observed as terminated, restored, observed as live again, and checked through tmux diagnostics.
- Tmux diagnostics: for each observed orchestrator, worker, worker-work, and reviewer session, the runner checks the tmux session, captures recent pane output, and flags visible blocking prompts. It records this as evidence only; it does not send keys or approve prompts.
- Issue diagnostics: when any stage fails or becomes `unobservable`, the final report adds a `diagnostics` section with fresh `ao status`, full session listing, per-session `ao session get`, worker `ao review ls`, tmux existence checks, recent pane captures, and pane signal tags such as `blocking-prompt`, `auth-or-login`, `rate-limit-or-quota`, `error-text`, `permission-denied`, `conflict`, `work-in-progress`, and `input-or-shell-prompt`.

Keep the report. It records commands, exit codes, stdout/stderr, timestamps, IDs, observations, the first failed stage, cleanup results, and failure diagnostics. Use `diagnostics.summary` first to see which session was blocked, missing tmux, waiting for input, failing auth, rate limited, or still doing work.

## Manual diagnosis

Run these with the verified binary:

```bash
ao version
ao status
ao project get <project> --json
ao orchestrator ls --json
ao session ls --project <project> --all --json
ao session get <session-id> --json
ao review ls <worker-session-id> --json
tmux has-session -t <session-id>
tmux capture-pane -t <session-id>:0.0 -p -S -160
```

Classify every finding:

- **Observed:** the command output or worker artifact directly proves it.
- **Failed:** an observable assertion contradicts the expected lifecycle.
- **Unobservable:** the current CLI/API does not expose the fact; do not infer it from spawn success.

For prompt delivery, prefer observed role behavior and AO-generated prompt artifacts under `<dataDir>/prompts/<session-id>/system.md`; do not ask agents to reveal their exact system prompt. For worker placement, inspect the worktree and branch recorded by AO, then verify the notification-icon diff and relevant frontend checks from that worktree. For Kanban alignment, capture tracker status and AO `activity.state` at each transition; do not treat a failed runtime probe as proof the worker is dead. For review, compare the worker PR owner, `ao review ls` latest run, reviewer session, submitted verdict, and any review comments.

## Failure interpretation

- `preflight`: wrong binary/daemon, missing project, missing credentials, or unsupported harness.
- `role-readiness`: a requested role harness is unsupported, not installed, needs authentication, or has authorization that cannot be proven from AO's fresh local probe.
- `orchestrator`: role spawn, prompt construction, or session startup failure after readiness passed.
- `delegation-and-worker`: orchestrator did not produce an inspectable worker in the project before timeout.
- `work-kanban-and-pr`: observable activity/status mismatch, missing PR evidence, or branch/worktree/tracker evidence unavailable through the CLI.
- `reviewer`: review run, reviewer session, or submitted verdict evidence missing before timeout.
- `lifecycle`: session kill did not terminate, restore did not bring the session back, or restored tmux/session evidence is missing.

Do not auto-retry externally visible actions. Do not kill or delete sessions unless the user explicitly selected cleanup. Escalate with the JSON report, the exact failed stage, and the `diagnostics.summary` entries for every affected session.

## Useful next modules

The current split covers the major path. The next valuable modules would be:

- Artifact/diff verification: inspect the worker worktree and assert the requested code change plus relevant checks, instead of stopping at PR/session evidence.
- Failure bundle export: collect the JSON report, captured diagnostics, branch, PR URL, and recent daemon logs into one directory for debugging.
- Recovery-after-review: verify changes-requested comments create a follow-up worker action and an updated PR/check state.
