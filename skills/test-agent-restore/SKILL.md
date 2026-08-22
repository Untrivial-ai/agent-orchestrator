---
name: test-agent-restore
description: Run a repeatable Agent Orchestrator CLI lifecycle test that spawns one or more agent sessions, kills them, restores them, verifies their final state, and reports restored versus failed counts with per-session reasons. Use when asked to test AO session restore behavior, reproduce the New task → Kill session → Restore workflow, batch-check agent restoration, or produce a restore reliability report.
---

# Test Agent Restore

Run the bundled deterministic CLI driver and summarize its report. The test creates real AO sessions, kills them, and leaves successfully restored sessions running for inspection.

## Start the desktop app

The AO desktop app owns the daemon. Before spawning any session, the runner checks `ao status --json`; if the daemon is not `ready`, it runs `ao start` and waits for readiness. This can open the desktop app. A startup failure exits 2 before any agent is counted as a failed restore.

## Run the test

1. Determine the requested agent count. Use 3 when the user does not specify one.
2. State the count, project, and that successful restores remain active before running.
3. Resolve the runner relative to this `SKILL.md` and execute:

```bash
node scripts/run_restore_test.mjs \
  --count 3 \
  --project agent-orchestrator \
  --report /tmp/agent-restore-report.json
```

The defaults match the development setup:

- AO binary: `$AO_BIN`, otherwise `/tmp/ao`, otherwise `ao` from `PATH`
- Run file: `$AO_RUN_FILE`, otherwise `~/.ao/dev/running.json`
- Data directory: `$AO_DATA_DIR`, otherwise `~/.ao/dev/data`
- Prompt: `Create RESTORE_TEST.md, then wait for further instructions.`
- Name prefix: `restore-test`

Pass `--ao`, `--run-file`, `--data-dir`, `--prompt`, or `--name-prefix` only when the user requests a different setup. Use `--settle-seconds` to change the short delay between spawn and kill, or `--daemon-start-timeout-seconds` to change the 60-second readiness wait.

## Interpret results

Treat a session as restored only when all of these are true:

1. `ao spawn` succeeds and returns a session ID.
2. `ao session kill` succeeds.
3. `ao session get --json` confirms `isTerminated: true`.
4. `ao session restore` succeeds.
5. A final `ao session get --json` confirms the same session ID, project, and `isTerminated: false`.

The runner exits 0 only when every requested agent is verified restored. Exit 1 means at least one lifecycle failed. Exit 2 means the test could not start because of invalid configuration.

Report:

- requested, restored, and not-restored counts;
- session ID and result for every attempt;
- the failed stage and CLI/API error text for every failure;
- the JSON report path, when requested;
- that restored sessions remain running and may need manual cleanup.

Do not claim success from the restore command alone; use the final JSON verification. Do not delete worktrees, kill restored sessions, or retry failed restores automatically unless the user asks.

## Diagnose failures

Use the failed stage to explain the cause:

- `spawn`: project, daemon, agent authentication, or spawn failure.
- `spawn-parse`: unexpected CLI output; include the captured output.
- `kill`: termination request failed.
- `verify-killed`: the session could not be read or was not terminated.
- `restore`: AO rejected or failed the restore; preserve its error envelope text.
- `verify-restored`: restore returned success but the final session state was missing, mismatched, or still terminated.

If the cause remains ambiguous, inspect the JSON report's recorded command, exit code, stdout, and stderr. Label any further explanation as an inference.
