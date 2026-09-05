# ChatUI regression harness

This harness preserves the failures found in the 2026-08-25 ChatUI audit as
repeatable contracts. It is deliberately opt-in: it is useful while the known
regressions are red, but it is not part of the normal CI path yet.

There are two different test lanes:

- **Deterministic contracts** use the browser renderer, the controlled bridge,
  and tagged Go contract tests. They require no model credentials and should
  produce the same answer on every run.
- **Packaged-Electron/live canaries** exercise the actual desktop bundle, bundled
  daemon, native terminal runtime, and authenticated coding-agent providers.
  They validate integration boundaries the deterministic lane cannot reproduce,
  but provider availability and output make them canaries rather than a
  deterministic gate.

The machine-readable live catalog is
[`frontend/chatui-regression/live/scenarios.json`](../../frontend/chatui-regression/live/scenarios.json).
Original observations, screenshots, and generated captures are intentionally
kept outside the repository. The tracked issues are the durable source for each
finding; new runs write evidence beneath the gitignored `e2e-artifacts/` tree.

## Quick start: deterministic contracts

From the repository root, capture the current red baseline without making the
command block later checks:

```bash
npm run qa:chatui -- --capture
```

Run the same contracts as a strict regression gate:

```bash
npm run qa:chatui
```

Useful filters are forwarded to Playwright:

```bash
npm run qa:chatui -- --headed
npm run qa:chatui -- --grep MQA-05
```

The frontend-level direct entry point is available for harness development:

```bash
npm --prefix frontend run test:e2e:chatui -- --capture
```

The repository-root `qa:chatui` command is the supported human-facing entry
point. The frontend command is an implementation detail and may change when the
runner is reorganized.

### Prerequisites

- Node.js and npm compatible with the checked-in lockfiles.
- The Go version required by [`backend/go.mod`](../../backend/go.mod).
- Frontend dependencies installed with `cd frontend && npm ci` when they are not
  already present.
- The Playwright Chromium binary installed with
  `cd frontend && npx playwright install chromium` when Playwright reports it
  missing.

The deterministic lane does not require Codex, Claude Code, Cursor, or any
provider credentials. It starts its own renderer server on an isolated loopback
port and must not reuse an already-running Vite server.

## Capture mode versus strict mode

Once the harness has initialized, both modes execute every gate selected by the
command (all gates when unfiltered) and retain evidence. They differ only in how
completed contract failures are reported to the caller. Argument,
harness-startup, and artifact-persistence failures are infrastructure failures
and remain non-zero in both modes.

| Mode | Command | Failed assertions | Process exit | Intended use |
| --- | --- | --- | --- | --- |
| Capture | `npm run qa:chatui -- --capture` | Recorded as `captured_failures`; never relabelled as passing | Zero only after a completed contract run and persisted `summary.json`; infrastructure failures remain non-zero | Establish and compare a known-red baseline |
| Strict | `npm run qa:chatui` | Recorded as `failed` | Non-zero | Certify a fix and prevent regression |

Capture mode is not a pass. Read `summary.json` and check both `outcome` and
`passed`; an exit code of zero in capture mode means every gate ran and the
aggregate evidence was persisted, not that the contracts passed. If no readable
summary exists, the capture did not complete and must be treated as an
infrastructure failure. A fixed scenario must pass in strict mode before it is
considered certified.

A persisted run with a ChatUI typecheck failure, a process that could not start,
or missing configured Playwright reports uses `outcome: "infrastructure_failed"`
and exits 2 even under `--capture`.

For an unfiltered run, the artifact gate also verifies that `MQA-01` through
`MQA-12` each produced a completed Playwright result. Every completed browser
contract must persist its configured screenshot, video, and trace beneath that
run's artifact directory. Missing coverage, unreadable reports, suite-level
startup errors, zero matching tests, and missing evidence are infrastructure
failures rather than captured product failures.

The Go lane is equally explicit: the runner consumes `go test -json` output and
requires start plus terminal results for all three configured backend contracts
in an unfiltered run, or each mapped contract selected by `--grep`. A compile
failure, zero-test match, skipped test, or interrupted/incomplete test is an
infrastructure failure even if capture mode was requested.

The deterministic runner does not short-circuit after the first red step. An
unfiltered run executes the local-only frontend ChatUI typecheck, every
Playwright contract, and every configured Go contract so one failure cannot conceal
another. With `--grep`, the same expression selects Playwright contracts and any
configured Go contracts mapped to those scenarios; a scenario with no backend
companion omits the Go step. The backend contracts can also be run
directly:

```bash
cd backend
go test -tags chatui_regression \
  ./internal/httpd/controllers ./internal/session_manager ./internal/service/chat \
  -count=1 \
  -run '^(TestChatUIRegressionEncodedSyntheticBranchIDIsDecoded|TestChatUIRegressionTUIToChatRetriesFreshTargetAfterUnsettledHistory|TestInterfaceHandoffImportsOutcomeUnknownNativeHistoryAsRecovered)$' \
  -v
```

## Artifacts

Each deterministic invocation creates a timestamped directory:

```text
e2e-artifacts/chatui-regression/<ISO timestamp>/
├── summary.json
├── summary.md
├── run.log
├── logs/
│   ├── frontend-chatui-typecheck.log
│   ├── playwright-contracts.log
│   └── go-contracts.log
└── playwright/
    ├── results.json
    ├── html/index.html
    └── test-results/
        └── <per-test screenshots, video, and trace.zip>
```

Screenshots, video, and traces are retained for passing tests as well as failures
so a red-before/green-after comparison uses equivalent evidence. Do not commit
this artifact tree.

A packaged-Electron run should write a sibling tree under
`e2e-artifacts/chatui-regression/live/<run-id>/` containing:

- a sanitized run manifest with repository commit, package version, architecture,
  scenario IDs, provider versions, fixture commit, daemon PID/port, and outcome;
- the Playwright trace, screenshots, and video for every scenario;
- Electron main-process and daemon logs;
- renderer console, `pageerror`, and failed-request JSONL streams;
- before/after daemon API snapshots for the session, conversation, interface
  transition, and agent switch involved;
- the fixture clone's `git status`, `git diff --binary`, and worktree list; and
- a cleanup receipt listing every session ID and exact process checked.

Never copy tokens, cookies, complete environments, provider configuration files,
or prompt-bearing private logs into artifacts. Record an allowlist of versions and
non-secret paths instead.

## Coverage of the 12 audit issues

Every issue has a deterministic browser companion in the implemented suite. For
eight issues that contract is also the full-certification lane. `MQA-01`,
`MQA-03`, `MQA-08`, and `MQA-10` additionally require a primary authenticated
packaged-Electron canary because their complete defect includes provider or native
runtime behavior that a controlled renderer cannot prove. A supplemental canary
is a second confidence check, not a replacement for the deterministic contract.

| Issue | Full-certification lane | Companion coverage | Regression contract |
| --- | --- | --- | --- |
| `MQA-01` | Packaged Electron/live | Deterministic disclosure contract | Plan Mode copy and filesystem effects match the actual provider boundary |
| `MQA-02` | Deterministic | Optional live queue turn | Stopping active work never silently discards accepted queued work |
| `MQA-03` | Packaged Electron/live | Deterministic disclosure contract | Agent-switch claims match durable semantic-handoff facts |
| `MQA-04` | Deterministic | Packaged cold restart | Session drafts survive unmount, navigation, reload, and promised restart boundaries |
| `MQA-05` | Deterministic | Packaged busy provider | Busy Chat shows a policy choice before any destructive interface-transition request |
| `MQA-06` | Deterministic | Packaged Claude recovery canary | An unsettled fresh target is retried/recovered without poisoning the session |
| `MQA-07` | Deterministic | Optional packaged branch flow | Every advertised previous/next branch ID is activatable |
| `MQA-08` | Packaged Electron/live | Deterministic imported-label contract | Imported successful terminal turns receive truthful completion labeling |
| `MQA-09` | Deterministic | — | At-sign chips identify path references rather than embedded context |
| `MQA-10` | Packaged Electron/live | Deterministic payload companion | Delivery mode is disclosed and original attachment identity remains durable |
| `MQA-11` | Deterministic | VoiceOver spot check | Queue/Steer and turn settings expose selected-state semantics |
| `MQA-12` | Deterministic | Packaged terminal lifecycle | Uncaught renderer/xterm errors fail strict runs and no lifecycle error is emitted |

All 12 issues therefore run in the deterministic browser suite. Eight can be
fully certified there (`MQA-02`, `MQA-04`, `MQA-05`, `MQA-06`, `MQA-07`,
`MQA-09`, `MQA-11`, and `MQA-12`); four retain primary live canaries (`MQA-01`,
`MQA-03`, `MQA-08`, and `MQA-10`) for the boundary the browser companion cannot
observe.

Strict certification also requires both `GAP-01` observability contracts found
during the audit: mounted context/quota signals and actionable credit-exhaustion
detail. These are additional required checks, so a strict run remains red until
they pass even when every `MQA-01` through `MQA-12` contract is green.

The deterministic `MQA-06` contract injects unsettled history and checks retry
convergence. Its live Claude canary remains a high-value confidence check because
the original failure survived a native recovery turn, daemon/Electron restart, and
identity refresh.
Likewise, deterministic `MQA-12` verifies harness failure semantics while its
supplemental canary answers whether the xterm race exists in the packaged app.

## Live catalog contract

`scenarios.json` is data, not prose. A live driver must:

1. reject an unknown `schemaVersion`, lane, action operation, or assertion;
2. run scenarios serially and create a distinct session ID for each scenario;
3. mark missing authentication, unsupported provider capability, or absent
   isolation seam as `blocked`, never `passed`;
4. execute every action in order and evaluate every expected result;
5. capture every evidence item marked `required`; and
6. preserve observed failures in capture mode while returning non-zero for them
   in strict mode.

Free-form interpretation is intentionally not part of the format. The `operation`
and `assertion` values are stable dispatch keys; descriptions are for operators.
`providersByLane` is required on every scenario. Its keys must exactly cover the
primary `lane` plus every `supplementalLanes` entry, and each value lists the
providers required in that lane. `scripted-chat-driver` is valid only in the
deterministic lane; a packaged-Electron lane must name an installed, authenticated
provider.

## Safe fixture preparation

The source todo repository is read-only input. Never register it directly with the
certification daemon, create AO metadata in it, clean it, switch its branch, or
modify its worktree. A local clone includes committed `HEAD` only, so dirty and
untracked user files remain untouched and are intentionally absent from the test.

From the repository root:

```bash
chatui_source_repo="${AO_CHATUI_E2E_REPO:-/Users/dhruvsharma/Development/todo-app}"
chatui_run_id="chatui-live-$(date -u +%Y%m%dT%H%M%SZ)-$$"
chatui_run_root="$(mktemp -d "${TMPDIR:-/tmp}/ao-chatui-live.XXXXXX")"
chatui_fixture="$chatui_run_root/fixture/todo-app-$chatui_run_id"

git -C "$chatui_source_repo" rev-parse HEAD
git -C "$chatui_source_repo" status --short
mkdir -p "$chatui_run_root/fixture"
git clone --local --no-hardlinks "$chatui_source_repo" "$chatui_fixture"
git -C "$chatui_fixture" config user.name "AO ChatUI certification"
git -C "$chatui_fixture" config user.email "chatui-certification@invalid.example"
```

Record the source commit and status in the run manifest, but never fail merely
because the source has user-owned dirty files: they are not copied. Register only
`$chatui_fixture` through the real app/daemon and use a globally unique project and
session ID. Capture the clone's final status and diff before deleting the run
directory.

## Packaged Electron: isolation prerequisite

Do not automate the real desktop app yet unless the build contains an explicit,
test-only Electron `userData` override.

Today `frontend/src/main.ts` pins packaged Chromium state and the
single-instance lock to the production path derived from `os.homedir()`, regardless
of `AO_DATA_DIR`. `AO_DATA_DIR`, `AO_RUN_FILE`, and `AO_PORT` therefore isolate the
daemon but **do not isolate Electron cookies, local/session storage, cache, crash
dumps, or the single-instance lock**.

The required seam should be named `AO_E2E_USER_DATA_DIR`, accepted only when
`AO_CHATUI_E2E=1`, require an absolute path, and be applied before
`app.setPath("userData", ...)`. Production behavior must remain unchanged when the
gate is absent.

**Never set, replace, or repurpose `HOME` to obtain Electron isolation.** Changing
the process home also changes git, shell, provider, and tool behavior and can make
a test appear isolated while silently using the wrong credentials or config.
Automation must stop as `blocked` if the explicit `userData` seam is unavailable.

Packaged AO currently also selects the shared tmux socket name `ao`. Until a
test-only socket override is supported, run live certification serially, use unique
session IDs, and kill every created session through the daemon API before quitting
the app. Never run `tmux kill-server`.

## Safe manual packaged-Electron procedure on macOS

These steps are intentionally manual until the `AO_E2E_USER_DATA_DIR` seam exists.
They launch the checkout's real packaged app, not `dev:web`, a normal browser, or
the installed production app.

### 1. Verify prerequisites

- macOS with the repository's Node/npm and Go toolchains available.
- `frontend/node_modules` installed.
- The todo source repository readable at `AO_CHATUI_E2E_REPO` or the default path
  shown above.
- Dedicated, already-authenticated test provider profiles supplied through
  `CODEX_HOME` and/or `CLAUDE_CONFIG_DIR` for the selected scenarios. Do not copy
  those profiles into the artifact directory.
- No other live-certification run using the shared `ao` tmux socket.
- The explicit Electron isolation seam present:

```bash
rg -n 'AO_E2E_USER_DATA_DIR' frontend/src/main.ts
```

If that check does not find the gated `app.setPath` override, stop. Do not work
around it by changing `HOME`, and do not point a test at the real Electron profile.

### 2. Build and resolve the local package

From the repository root:

```bash
npm --prefix frontend run package

chatui_repo_root="$(pwd -P)"
chatui_arch="$(node -p 'process.arch')"
chatui_app_bin="$chatui_repo_root/frontend/out/Agent Orchestrator-darwin-$chatui_arch/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator"
test -x "$chatui_app_bin"
```

Invoke the executable directly. Do not use `open`, because that severs controlled
environment, PID, and standard-output ownership. A locally built unsigned bundle
can normally run directly; a downloaded/quarantined release can invoke Gatekeeper
and must be verified with `frontend/scripts/verify-mac-artifact.sh`, never by
hand-unzipping it.

### 3. Allocate isolated runtime paths

After preparing the fixture as described above:

```bash
chatui_port="$(node -e 'const net=require("net");const server=net.createServer();server.listen(0,"127.0.0.1",()=>{console.log(server.address().port);server.close();});')"
chatui_artifact_dir="$chatui_repo_root/e2e-artifacts/chatui-regression/live/$chatui_run_id"

mkdir -p "$chatui_run_root/data" "$chatui_run_root/electron" "$chatui_artifact_dir"
```

There is a small race between finding the free port and daemon bind. Attribute the
daemon with the unique run file as well: it must report the selected port,
`owner: "app"`, a start time after this launch, and the same PID from `/readyz`.

### 4. Launch in the foreground

```bash
env \
  AO_CHATUI_E2E=1 \
  AO_E2E_USER_DATA_DIR="$chatui_run_root/electron" \
  AO_DATA_DIR="$chatui_run_root/data" \
  AO_RUN_FILE="$chatui_run_root/running.json" \
  AO_PORT="$chatui_port" \
  AO_APP_RUN_ID="$chatui_run_id" \
  AO_KEEP_DAEMON=0 \
  AO_DISABLE_GPU=0 \
  AO_TELEMETRY_EVENTS=off \
  AO_TELEMETRY_REMOTE=off \
  AO_SENTRY_DSN= \
  CODEX_HOME="$CODEX_HOME" \
  CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR" \
  "$chatui_app_bin"
```

Supply only the provider variables needed by selected scenarios. The explicit
`AO_KEEP_DAEMON=0` and `AO_DISABLE_GPU=0` neutralize inherited shell values so the
daemon remains app-owned and normal hardware acceleration is certified. Keep the
process in the foreground and retain its PID/output. On macOS, run headed with
hardware acceleration enabled; Linux-only `--no-sandbox` and
`--disable-dev-shm-usage` flags do not belong in this procedure. Use
`AO_DISABLE_GPU=1` only in a separate diagnostic run for a known GPU startup
failure, not as the certification default.

For future automation, use Playwright's Electron API against this executable and
select the `app://renderer` page from `electronApplication.windows()`. Do not assume
`firstWindow()` because AO browser previews add `WebContentsView` instances.
Playwright's Electron support is experimental, though the repository's Playwright
1.60 line includes `WebContentsView` discovery. Use the composer's hidden file
input with `setInputFiles()`; Playwright does not control native macOS file dialogs.

### 5. Execute and capture scenarios

Run the selected entries from `scenarios.json` in order. Before each destructive
action, capture the listed `before-*` evidence. Use unique prompts and session IDs
so a response cannot be confused with stale history. Do not run implementation,
deployment, or network-side-effect prompts; the catalog uses bounded, read-only
markers.

A provider/auth problem is `blocked`. A mismatched expected result is `failed`.
Capture mode may return success to its caller after recording either result, but
the manifest must retain the true per-scenario outcome.

### 6. Clean up exact resources

Sessions survive app quit, so cleanup starts while the daemon is still reachable:

1. POST `/api/v1/sessions/{sessionId}/kill` for every session ID recorded by this
   run and confirm success.
2. Capture the fixture diff, final API state, daemon PID, and run-file contents.
3. Quit Electron with **Cmd+Q** and wait up to 15 seconds for the supervisor link to
   stop the app-owned daemon.
4. Confirm the recorded PID no longer exists, the exact port has no listener, and
   the run file is gone or no longer names a live process.
5. If a process remains, verify all three identities before acting: run-file PID,
   `/healthz` or `/readyz` PID, and a process command belonging to this exact
   packaged checkout. Terminate only that verified process group, first with TERM
   and only then with KILL after another bounded wait.
6. Preserve the artifact directory. Remove only the exact temporary run root after
   verifying its value and confirming no process uses it.

Never use `pkill Electron`, `pkill ao`, a broad process-name match, a port alone, or
`tmux kill-server`. Those can terminate the installed AO app, another worktree, or
user-owned sessions.

## Reading results

A scenario has one of four outcomes:

- `passed`: every expected result and required evidence item succeeded;
- `failed`: the product or harness violated an expected result;
- `blocked`: the requested lane could not run because an explicit prerequisite was
  absent; or
- `inconclusive`: actions completed but the required evidence could not distinguish
  pass from failure.

Only `passed` certifies a repair. Capture mode changes the aggregate process exit,
not these per-scenario outcomes.
