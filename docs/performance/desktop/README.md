# Desktop performance

Reproducible measurements for AO desktop cold start, daemon/renderer memory under
multi-session terminal load, and terminal input-to-echo latency. These numbers
answer the common "Electron orchestrators are slow / heavy" claim with data from
one machine, not a claim that AO is always faster than every competitor.

## Machine under test

| Spec | Value |
| --- | --- |
| CPU | Apple M4 (10 logical CPUs) |
| RAM | 16 GiB |
| OS | macOS 26.5 (darwin arm64) |
| Node | v22.23.2 |
| Date | 2026-09-19 |

Host was under memory pressure during the runs (`freeMemory` often &lt; 300 MiB),
so cold-start wall times vary across repetitions. Prefer the memory and latency
columns for comparisons; treat cold start as an order-of-magnitude figure.

## Reproduce

From the repo root (isolated temp `AO_DATA_DIR`; never touches `~/.ao`):

```sh
node scripts/desktop-perf-bench.mjs --label sample --out docs/performance/desktop
```

Optional Electron cold start + renderer RSS (requires `npm ci` in `frontend/`):

```sh
node scripts/desktop-perf-bench.mjs --electron --label sample-electron
```

Optional real `claude-code` sessions instead of shell terminals:

```sh
AO_PERF_USE_SESSIONS=1 node scripts/desktop-perf-bench.mjs --label sessions
```

Default load opens **N attached shell terminals** over the daemon mux. That is
the same PTY/WebSocket path session terminals use, and it is reproducible without
agent credentials or model spend.

## Method

| Metric | Definition |
| --- | --- |
| Daemon cold start → interactive | `ao daemon` spawn → `/readyz` 200 → `GET /api/v1/projects` 200 |
| Electron cold start → interactive | `npm run dev` → "Launched Electron app" and daemon still ready (`--electron`) |
| Daemon RSS @ N | `ps -o rss=` for the daemon PID after N mux-attached shells settle ~2.5s |
| Renderer RSS @ N | Sum of Electron Helper (Renderer) RSS in the forge process tree (`--electron` only) |
| Terminal input→echo | Mux WebSocket: write a unique `printf` marker; time until that marker appears in a data frame (PTY round-trip; predictive local echo excluded) |

Raw JSON: [`baseline-latest.json`](baseline-latest.json), [`after-latest.json`](after-latest.json).

## Results (daemon path, this machine)

Steady-state daemon RSS and input→echo did not move outside run-to-run noise when
the idle-poll / queue-cap fixes landed (those fixes are not on the echo hot path).
Published paired JSON: [`baseline-latest.json`](baseline-latest.json) (label only;
same build as after for the last pair), [`after-latest.json`](after-latest.json).

| Measurement | Observed on this host |
| --- | ---: |
| Daemon cold start → interactive | 2.3–8.0 s (earlier quiet run 2.3 s; later runs under &lt;300 MiB free RAM 4.9–8.0 s) |
| Daemon RSS @ 1 attached shell | 37–49 MiB |
| Daemon RSS @ 5 attached shells | 29–35 MiB |
| Daemon RSS @ 10 attached shells | 30–38 MiB |
| Terminal input→echo p50 | 50.9–51.4 ms |
| Terminal input→echo p95 | 52.7–58.2 ms |

| Fix area | Before → after (behavioral) |
| --- | --- |
| Settings HTTP while idle | Continuous 15 s poll → poll only until first success, then stop |
| Workspace board backup poll | 15 s → 60 s when statuses are settled |
| Cloud mux pending keystrokes | Unbounded → cap 64 |
| Daemon attach pending input | Unbounded → cap 64 KiB |

## Contained fixes shipped with this measurement

| Finding | Change |
| --- | --- |
| `useSettings` polled `/api/v1/settings` every 15s forever (mounted from TaskComposer / cloud gates) | Poll every 2s only until the first successful snapshot; then stop (`refetchInterval: false`). Mutations still invalidate. |
| Workspace board backup poll every 15s rebuilt list identity on an idle board | Idle backup interval 15s → 60s; keep 300ms while statuses are still checking. CDC remains the live path. |
| Cloud terminal mux queued keystrokes with no cap while the sandbox socket was down | Cap pending input at 64 entries (drop oldest). |
| Daemon terminal attachment buffered input during attach with no byte cap | Reject new chunks above 64 KiB pending (`terminal: pending input buffer full`) and release the input lease. |

These are preventive bounds and idle-churn cuts. They are not expected to move
steady-state echo latency or modest shell-count RSS; they stop pathological
growth under reconnect / paste storms and cut background HTTP while the UI is idle.

## Electron / renderer

`--electron` was not recorded in the published JSON pair above: the host had
&lt;300 MiB free RAM, and a forge cold start would have been dominated by swapping.
Re-run with `--electron` on a quiet machine to fill renderer RSS and desktop cold
start. Until then, do **not** treat scavenged Helper (Renderer) processes from
other apps as AO numbers.

## Deliberate limits

- Default load uses shell terminals, not paid agent sessions.
- Input→echo is mux/PTY RTT, not display scanout or predictive local echo.
- Single-machine descriptive stats (one run per label in the paired table); not
  population percentiles or a universal speedup claim.
