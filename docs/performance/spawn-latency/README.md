# Spawn / chat-driver latency

Opt-in benchmarks for the cold path that blocks AO's "Create a new task" UI.

| Workload | Coverage |
| --- | --- |
| **Chat driver Start** | Every shipped **chat** harness (`chatdriver/registry.Build`, ~10) |
| **Daemon spawn chat** | Same chat harnesses |
| **Daemon spawn TUI** | Every shipped **agent** harness (`agent/registry.Harnessed`, ~27) |
| **Git worktree add** | Fixture or `AO_PERF_REPO` |

Missing installs are skipped (not failed). These are **hill-climb evidence**, not CI gates.

Chat-only Start cannot cover TUI-only agents (no chat driver). TUI spawn covers the full agent matrix.

## Reproduce

From `backend/`:

```sh
# 1) Chat driver Start across installed chat harnesses
AO_PERF_BENCH=1 AO_PERF_LABEL=local AO_PERF_DIR="$HOME/.ao/validation/spawn-latency" \
  go test ./internal/adapters/chatdriver/registry/ \
  -run 'TestBenchChatDriverStart$' -v -count=1 -timeout 45m

# 2) Full spawn: chat matrix + TUI matrix (~all agents)
AO_PERF_BENCH=1 AO_PERF_LABEL=local AO_PERF_DIR="$HOME/.ao/validation/spawn-latency" \
  go test ./e2e/ -run 'TestBenchSpawn' -v -count=1 -timeout 90m

# 3) Subset + large repo
AO_PERF_BENCH=1 AO_PERF_HARNESS=cursor,claude-code,codex \
  AO_PERF_REPO=/path/to/agent-orchestrator \
  AO_PERF_LABEL=local-large AO_PERF_DIR="$HOME/.ao/validation/spawn-latency" \
  go test ./e2e/ -run 'TestBenchSpawn|TestBenchGitWorktreeAdd' -v -count=1 -timeout 60m
```

### Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `AO_PERF_BENCH` | unset | Must be `1` or tests skip |
| `AO_PERF_REPEAT` | `3` | Samples per harness/workload |
| `AO_PERF_LABEL` | `sample` | Label in JSON filenames |
| `AO_PERF_DIR` | unset | Write `<label>-<workload>.json` here |
| `AO_PERF_HARNESS` | full matrix | Comma list or `all` |
| `AO_PERF_REPO` | unset | Real git checkout for large-repo timings |

## Metrics

| Workload | Isolates |
| --- | --- |
| `chat-driver-start-<harness>` | Provider chat cold start only |
| `spawn-chat-empty-<harness>` | Full Spawn chat mode, empty prompt |
| `spawn-tui-empty-<harness>` | Full Spawn TUI mode (all agents) |
| `git-worktree-add` | Raw `git worktree add` |

Chat vs TUI on a tiny fixture shows each harness's chat premium. Large-repo runs via `AO_PERF_REPO` add fetch + worktree cost.

## Notes

- Empty prompts avoid first-turn model latency.
- Binary presence uses each adapter's `ResolveBinary` (same as production).
- Chat Start stays limited to chat drivers; adding a chat harness to `chatdriver/registry.Build` automatically includes it.
- TUI spawn follows `agent/registry.Constructors` — new agents are included automatically.
- Timings are host-dependent; compare before/after on the same machine.
