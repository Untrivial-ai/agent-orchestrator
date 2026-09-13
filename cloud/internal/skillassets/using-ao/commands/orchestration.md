# Orchestration: spawn, list, send, report, kill

All five commands talk to the AO control plane with this session's rotating
worker credential. `spawn`, `list`, `send`, and `kill` require the
`worker:orchestrate` scope, which only orchestrator sessions hold; `report`
requires a parent orchestrator. A command run without the required scope fails
with `SCOPE_REQUIRED`.

## ao spawn — create a worker session

```
ao spawn --name "<label>" --prompt "<clear worker task>" [--agent claude-code] [--mode standard|trusted]
```

- `--name` (required): the label the human sees in the sidebar. Keep it short
  and specific — 20 characters or fewer is ideal (80 is the hard cap). Count it
  before running the command; an over-long label wastes a turn on a rejection.
- `--prompt` (required): the worker's complete task. Write it like a brief for
  a competent engineer with no other context: goal, constraints, and the
  expected outcome (usually a pull request).
- `--agent` / `--harness`: `claude-code` (default), `codex`, or `cursor`.
- `--mode`: `trusted` (default) or `standard`.

Prints `spawned <session-id> (<status>)`. The worker provisions its own
sandbox, checks out the project repository on a fresh branch, and starts on the
prompt — typically live within ~10 seconds. You do not need to wait: a `send`
issued before it is ready is queued and delivered on startup.

Errors: `agent_provider_required` means the org has no validated credential for
that harness — pick a different `--agent` or report the blocker to the human.
`SANDBOX_QUOTA_EXCEEDED` means the org hit its sandbox cap — kill finished
workers first.

## ao list — inspect your workers

```
ao list [--json] [--all]
```

Shows the workers this orchestrator spawned: id, name, harness, branch,
status, whether the worker runtime is connected, and its pull request (number
and CI state) once one exists. Terminated workers are hidden unless `--all`.

`--json` includes per-PR detail (`prs`: url, number, state, ci, review,
mergeability, source/target branch) plus `activityState` and `isTerminated`.

Status vocabulary: `working`, `idle`, `needs_input`, `pr_open`, `draft`,
`ci_failed`, `review_pending`, `changes_requested`, `approved`, `mergeable`,
`merged`, `exited`, `terminated`.

## ao send — message a worker

```
ao send <session-id> <message...>
ao send --session <session-id> --message "<message>"
```

Delivers into the worker's conversation, exactly as if the human had typed it.
Queued durably: a worker that is still provisioning receives it when its agent
starts. Use it to route CI failures, review feedback, clarifications, and
course corrections.

## ao report — message your orchestrator (workers only)

```
ao report <message...>
```

Sends a message to the orchestrator session that spawned this worker. Use it
when the task is done (say what was delivered and the PR number), or when
blocked on a decision only the orchestrator or human can make. Keep it short;
the orchestrator sees it in its own conversation prefixed with this session's
id. A session that was not spawned by an orchestrator gets `SCOPE_REQUIRED`.

## ao kill — terminate a worker

```
ao kill <session-id>
```

Terminates the worker session and destroys its sandbox. Kill workers whose
task is complete (PR merged or handed off) or that are permanently stuck after
a redirect attempt. Killing is not reversible; the branch and any open PR
survive on GitHub.
