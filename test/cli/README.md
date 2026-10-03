# `ao` CLI end-to-end tests

These tests drive the **real `ao` binary** through its CLI and daemon-control
HTTP surface. The tagged suite starts the hidden daemon command, then checks
status, doctor, lifecycle, and stop behavior. The tests use **isolated,
throwaway state** (a per-test temp run-file and data directory plus an
OS-assigned free loopback port), so they never touch a developer's real AO
installation. The desktop-app launcher behavior of `ao start` is covered by
the fresh-install check below, not by this daemon suite.

## Two tiers

| Tier                          | What                                                                                                                                                                                                                                                                  | Where                                                |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| **Comprehensive (primary)**   | A cross-platform Go suite that builds `ao` and exercises the daemon and CLI lifecycle against isolated state. Runs natively on **ubuntu + macOS + windows**.                                                                                                    | `backend/internal/cli/e2e_test.go` and `e2e_sigpipe_unix_test.go` (build tag `e2e`) |
| **Fresh-install (hardening)** | Proves a clean installation can run `version` and `doctor`, then reaches the expected `ao start` release-fetch error without a panic or silent success.                                                                                                        | `test/cli/Dockerfile` + `test/cli/install-check.sh`  |

## Run it

**The Go suite (fastest, cross-platform):**

```bash
cd backend
go test -tags e2e ./internal/cli/...              # run it
go test -tags e2e -v -run TestE2E ./internal/cli/...   # verbose: prints every command + output
```

It builds its own `ao` binary; `git` must be on PATH (required by `doctor`).
`-v` logs each `ao` invocation and its full output, which is the audit trail you
get for free from `go test`.

**Fresh-machine install, in a clean container:**

```bash
docker build -f test/cli/Dockerfile -t ao-cli-smoke .
docker run --rm --init ao-cli-smoke
```

> `--init` gives the container a real PID-1 reaper (tini) for any short-lived
> child process. The fresh-install check does not start a daemon; it verifies
> that `ao start` reaches its expected release-fetch failure cleanly.

## What the Go suite covers

`TestE2E_VersionAndHelp` (version/`--version`/help, daemon hidden) ·
`TestE2E_DoctorDoesNotTouchTheStore` (doctor text + `--json`; proves it does
**not** create/migrate `ao.db`) · `TestE2E_StatusStopped` (stopped + idempotent
stop) · `TestE2E_Lifecycle` (daemon startup, ready, idempotent, daemon-created store,
`/healthz` identity, stop, run-file cleanup) · `TestE2E_ShutdownGuard` (the
`/shutdown` CSRF + DNS-rebinding 403 guard, daemon survives) ·
`TestE2E_StaleRunFile` (dead-PID run-file → stale → cleaned) · `TestE2E_ExitCodes`
(2 usage / 1 runtime / config error) · `TestE2E_Completion` (all four shells).
On Unix, the tagged suite also runs
`TestE2E_DaemonClosedOutputPipeShutdownRemovesRunFile`, which verifies that a
daemon survives a closed output pipe and still cleans up its run-file.

The normal `go test ./...` lane adds loopback HTTP contract coverage for the
CLI's `spawn` and `project add` request DTOs, plus metadata-only PR claims.

## Why a Go suite (not bash, not Python)

The bash version grew past the point where bash was a good fit, and a Linux
container can't exercise the macOS/Windows runners. A Go `os/exec` suite uses
the repo's own toolchain, gives real assertions and structured data, and runs
natively on each CI operating system. The suite sets `AO_RUN_FILE` and
`AO_DATA_DIR` for isolation, so default config-directory resolution belongs in
separate tests. The container stays as a thin clean-install check.

## Extending

- **Add a case:** a new `TestE2E_*` function (or a `t.Run` subtest) in an
  appropriate `backend/internal/cli/*_test.go` file. Use `newEnv(t)` for
  isolated state and the `env.run`/`httpGet`/`postShutdown` helpers.
- **Add an OS:** extend the `matrix.os` list in `.github/workflows/cli-e2e.yml`.
- Assertions for default state paths under `~/.ao`
  when `AO_RUN_FILE`/`AO_DATA_DIR` are unset belong in unit tests in
  `internal/config`; this suite intentionally overrides both paths.
