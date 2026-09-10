# AGENTS.md

Operational guidance for coding agents working in this repository. Keep changes small, match the current rewrite architecture, and prefer the documented daemon/API boundaries over behavior from the old TypeScript implementation.

## Repo layout

- `backend/` — Go rewrite of Agent Orchestrator: Cobra `ao` CLI, loopback HTTP daemon, services, SQLite storage, lifecycle/reaper, runtime/workspace/agent/tracker adapters, terminal mux, and tests.
- `frontend/` — Electron + React supervisor wired to the daemon via the generated typed client. Treat it as a thin supervisor/UI surface; do not move daemon logic into it.
- `docs/` — current architecture/status notes. Start here before changing lifecycle, CLI, agents, storage, or daemon behavior.
- `test/` — external smoke/e2e assets, including the CLI fresh-install container check.
- `.github/workflows/` — CI definitions. Mirror these commands locally when possible.

## Commands

From the repo root unless noted:

```bash
npm run lint                         # backend go test ./... + golangci-lint v2.12.2
npm run frontend:typecheck           # frontend TypeScript check
npm run sqlc                         # regenerate backend/internal/storage/sqlite/gen from queries/schema
npm run api                          # regenerate OpenAPI spec + frontend TS types (see API contract changes below)
npx @redwoodjs/agent-ci run --all    # local workflow validation; requires Docker socket
```

Backend-specific checks:

```bash
cd backend
go build ./...
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ao start
```

Frontend-specific checks:

```bash
cd frontend
npm run typecheck
npm run build
```

When showing or demoing frontend changes, run `ao preview [url]` from inside the session so the change renders in the desktop browser panel (the inspector rail's Browser tab); do not just describe it.

## Where to look first

- `README.md` — current run/config/test quickstart.
- `docs/README.md` — docs index.
- `docs/architecture.md` — backend mental model, package layout, lifecycle/session/service boundaries, and load-bearing rules.
- `docs/STATUS.md` — what is shipped on `main` today and what is still in flight.
- `docs/cli/README.md` — intended CLI shape: thin Cobra client over daemon HTTP, never direct storage/runtime access.
- `CLAUDE.md` — compatibility pointer for Claude Code; it directs agents back to `AGENTS.md`.

For code entry points:

- CLI commands: `backend/internal/cli/*.go`; follow nearby command/test patterns before adding a new style.
- HTTP controllers and DTOs: `backend/internal/httpd/controllers/`.
- Service read/write boundaries: `backend/internal/service/`.
- Domain vocabulary: `backend/internal/domain/`.
- Port contracts: `backend/internal/ports/`.
- SQLite queries/migrations/store: `backend/internal/storage/sqlite/`.
- Generated sqlc code: `backend/internal/storage/sqlite/gen/`.

## Distribution

- The **desktop app** (GitHub Releases) is the canonical, auto-updating install path. Point users there first.
- **npm still works but is no longer recommended.** `0.10.0` is the final version published to npm; the `@aoagents/ao` package is frozen and will not receive further updates. It remains a legacy on-ramp for users who already have `ao` on their PATH, where `ao start` fetches and opens the desktop build. Do not add features, docs, or flows that treat npm as the intended way to install AO.
- **Exactly one publisher.** Only the designated release conductor runs a real publish, on any channel. Divergent artifacts from multiple publishers made the 28-29 Jul macOS incident unreadable. Use the fork dev loop for test builds. Full rule and rationale: `frontend/docs/desktop-release.md`, "Hard rule: exactly one publisher".
- **Verify macOS artifacts with `frontend/scripts/verify-mac-artifact.sh`, never by hand.** It extracts with `ditto -x -k` and runs `codesign --verify --deep --strict`, `spctl -a -vv -t exec`, `xcrun stapler validate`. Plain `unzip` breaks the seal and yields a convincing false failure; `spctl` without `-vv` prints nothing at all on success.
- **macOS ships both a `.zip` and a `.dmg`.** The dmg is first install only. The zip and `latest-mac.yml` must keep publishing forever: electron-updater cannot install an update from a dmg. macOS differential updates are permanently disabled (full download only); see issues #3151 and #3267.

## Coding conventions

- Keep every change surgical and directly tied to the task. Avoid drive-by cleanup, broad renames, formatting churn, speculative abstractions, and architectural refactors unless the task explicitly asks for them.
- Follow existing Go package boundaries. CLI code should call daemon HTTP routes through shared CLI client helpers; it should not open SQLite, spawn runtimes, or call adapters directly.
- Keep Cobra commands in the relevant command file and table-test them in the style of `backend/internal/cli/*_test.go`.
- Mirror existing response/request DTOs in the CLI instead of importing HTTP controller packages into CLI code, unless the package already establishes that dependency.
- Return usage errors as `usageError` so CLI misuse exits 2; runtime/daemon failures should exit 1.
- Preserve API error envelopes and request IDs when surfacing daemon errors.
- Use `context.Context` as the first argument for functions that do I/O or blocking work.
- Do not add abstractions for one-off use cases. Add helpers only when they remove duplication across real call sites.
- Tests should cover the user-visible behavior and boundary being changed: happy path, validation/missing args, daemon error envelopes, and any destructive confirmation path.

## Hard rules and boundaries

- The daemon's **primary (loopback) listener** stays bound to `127.0.0.1` and unauthenticated. Do not change its bind host or add auth to it.
- The daemon MAY run a **second, opt-in LAN listener** (the "Connect Mobile" feature) that binds `0.0.0.0` **only while explicitly enabled**, **only** behind the bearer-password `authMiddleware`, serving the app API but never the loopback-gated control routes (`/shutdown`, telemetry, mobile control). **Exactly one route is exempt from `authMiddleware`: `GET /api/v1/identity`**, which returns an opaque host id and the mobile contract version so a phone can confirm which machine answered before presenting a credential — see `docs/adr/0003-unauthenticated-identity-probe.md`. The exemption is an exact path, `GET` only, and checked ahead of the lockout; any further unauthenticated route needs its own ADR. It is plaintext and home-network-only by deliberate decision — see `docs/adr/0001-lan-listener-for-mobile.md` and `CONTEXT.md`. Do not add any other network-facing bind.
- The CLI is a thin client. Do not port old in-process TypeScript CLI behavior that bypasses daemon HTTP routes.
- Do not store derived/display session status. Status is derived from durable facts (`activity_state`, `is_terminated`, PR/check/comment facts) at service read time.
- Do not treat failed/unknown runtime probes as proof a session is dead.
- Do not force-delete dirty registered worktrees.
- Do not modify already-merged SQLite migrations. Add a new migration instead.
- Do not hand-edit `backend/internal/storage/sqlite/gen/*`; change `backend/internal/storage/sqlite/queries/*` or migrations and run `npm run sqlc`.
- SQLite change events come from DB triggers into `change_log`; do not add parallel manual CDC emission from store methods unless the architecture changes explicitly.
- Keep generated OpenAPI/API DTO drift in mind: controller response shapes live in `backend/internal/httpd/controllers/dto.go` and tests may assert CLI/HTTP wire compatibility.
- Do not add network calls to tests unless the package already has an integration/e2e pattern for them. Prefer `httptest`, fakes, and injected dependencies.
- Do not commit local run state, daemon data, temporary worktrees, build outputs, or credentials.
- All AO-owned app state lives under `~/.ao` only. The daemon's data dir, `running.json`, worktrees, and the Electron supervisor's `userData` (Chromium cache, cookies, local/session storage, crash dumps) must resolve under `~/.ao` (overridable via `AO_DATA_DIR`/`AO_RUN_FILE`). Never write to or otherwise use `~/Library/Application Support` or any other OS default app-data location for AO state. The sole read exception is an explicit, user-initiated browser-profile import: it may read only validated, known source-browser profile files, must never write to the source profile, must keep source paths out of the renderer, and must place every snapshot, staging file, and imported result under `~/.ao`. `main.ts` pins Electron's `userData` to `~/.ao/electron`; do not remove that override or rely on Electron's default path.

## API contract changes

The daemon API is code-first. The OpenAPI spec and frontend TypeScript types are generated artifacts — edit the source, then regenerate.

**Source files to edit:**

- `backend/internal/httpd/controllers/dto.go` — request/response shapes.
- `backend/internal/httpd/apispec/specgen/build.go` — operation registry; add a `schemaNames` entry for any new named type.

**Regenerate after editing:**

```bash
npm run api          # runs api:spec then api:ts in sequence
```

This is equivalent to running:

```bash
npm run api:spec     # cd backend && go generate ./internal/httpd/apispec/...
npm run api:ts       # npx openapi-typescript@7.4.4 backend/internal/httpd/apispec/openapi.yaml -o frontend/src/api/schema.ts
```

**Verify:**

```bash
cd backend && go test ./internal/httpd/...    # spec drift + route/spec parity tests (does not cover schema.ts — that is checked by the api-drift CI job)
```

Commit `openapi.yaml` and `frontend/src/api/schema.ts` together with the Go changes. CI will regenerate both files and fail if the committed versions are out of date. The CLI hand-mirrored DTOs remain a deliberate manual boundary and are not generated.

## PR hygiene

- Before creating or handing over a PR, run all CI validation jobs locally using the workflow commands, pinned runtimes, and CI environment (including complete suites, not only focused tests). Fix failures and rerun the affected full suites before sharing the PR. A local pass is not a guarantee: verify the remote checks too. If a job cannot run locally (for example, an unavailable native OS runner, Docker, or required credentials), explicitly report the exact gap and verify that job in CI; never label it locally passed. Do not execute publishing or production deployment as a validation step.

- Branch from `main` unless explicitly continuing an existing PR.
- Keep one issue per PR. If asked for separate work, create a separate branch and PR.
- Use conventional commit messages (`feat:`, `fix:`, `docs:`, `test:`, `chore:`).
- Explain intentional omissions in the PR body, especially when the TypeScript original had more behavior than the Go rewrite domain currently supports.
- Run the narrowest relevant tests first, then the repo/CI commands that match the touched area.
