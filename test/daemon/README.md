# Daemon end-to-end checks

Small, linear shell checks that exercise the real `ao daemon` process. The
comprehensive, deterministic coverage lives in Go under `backend/internal/`;
these prove the assembled binary behaves at runtime.

## `cwd-stability-check.sh`

Regression guard for [#2871] (RCA [#2780], symptom [#2775]): a desktop
auto-update could leave the daemon in a deleted working directory, poisoning
every subsequent session spawn until `tmux kill-server`.

Verifies end-to-end that:

1. A daemon launched from a directory that is then deleted stays healthy, has
   `chdir`'d to its data dir, and preserves its startup cwd in the `/healthz`
   probe (`startupWorkingDirectory`).
2. A relative `AO_DATA_DIR` is absolutized at load time (no `reldata/reldata`
   double-nest after the daemon `chdir`s).
3. A tmux launch command's `cd <workspace> || exit` guard lands the pane in the
   workspace even against a tmux server whose own cwd was deleted.

```bash
# ao on PATH:
test/daemon/cwd-stability-check.sh

# or point at a built binary:
AO_BIN=./backend/ao test/daemon/cwd-stability-check.sh
go build -o ./ao ./backend/cmd/ao && ./test/daemon/cwd-stability-check.sh ./ao
```

Requires `curl`; the tmux check is skipped when `tmux` is absent. Test 3 asserts
the guard produces the correct cwd; the `-c`-ignored failure mode it defends
against is tmux-version/OS specific and may not reproduce on every host.

[#2871]: https://github.com/AgentWrapper/agent-orchestrator/pull/2871
[#2780]: https://github.com/AgentWrapper/agent-orchestrator/issues/2780
[#2775]: https://github.com/AgentWrapper/agent-orchestrator/issues/2775
