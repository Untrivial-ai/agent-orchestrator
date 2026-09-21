# Safe Harness Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make PR #4221's harness installer safe, daemon-verified, durable, diagnosable, and compatible with current `main`.

**Architecture:** The daemon owns installer recipes, capability preflight, durable jobs, execution, and adapter-backed verification. SQLite stores the latest job per harness; HTTP exposes method discovery, batch job hydration, install, and verify operations; React renders those facts and never decides success itself.

**Tech Stack:** Go, SQLite/sqlc, loopback HTTP/OpenAPI, React/TypeScript, Vitest/Testing Library, Electron.

**Spec:** `docs/superpowers/plans/2026-08-31-safe-harness-installer-design.md`

## Global Constraints

- Never use `sudo` or a `curl | shell` pipeline. Official HTTPS scripts must be downloaded completely with size, redirect, and timeout bounds before AO executes the saved file with a fixed interpreter.
- Installer stdin is closed and recipes use noninteractive modes where supported.
- The client sends only a server-issued method identifier, never argv or shell text.
- Verification resolves the exact binary through the canonical harness adapter and does not probe authentication.
- Current `main` owns newer Settings, daemon wiring, migrations, and generated artifacts during conflict resolution.
- Active Droid sessions block Droid install and reinstall; the installer never kills their processes.

---

### Task 1: Integrate current main without regressing its architecture

**Files:**
- Modify conflicts reported by `git merge untrivial/main`
- Preserve: `frontend/src/renderer/components/settings/HarnessSettingsSection.tsx`
- Preserve and adapt: `backend/internal/service/systeminstall/*.go`

**Interfaces:**
- Consumes: canonical `untrivial/main`
- Produces: a conflict-free PR branch using current Settings and daemon extension points

- [ ] Fetch `untrivial/main`, merge it into the PR branch, and list every conflict before editing.
- [ ] Resolve Settings conflicts by retaining current `main`'s shell/navigation and inserting the harness section at its current extension point.
- [ ] Resolve daemon/service conflicts by retaining current `main` wiring and adapting the installer constructor to its canonical agent resolver and stores.
- [ ] Run `git diff --check`, frontend typecheck, and focused existing installer tests to expose semantic integration failures.
- [ ] Commit the conflict resolution separately as `merge: sync harness installer with current main`.

### Task 2: Persist recoverable installation jobs

**Files:**
- Create: `backend/internal/storage/sqlite/migrations/0120_agent_install_jobs.sql`
- Create: `backend/internal/storage/sqlite/queries/agent_install_jobs.sql`
- Create: `backend/internal/storage/sqlite/store/agent_install_job_store.go`
- Create: `backend/internal/storage/sqlite/store/agent_install_job_store_test.go`
- Modify generated: `backend/internal/storage/sqlite/gen/*`
- Modify: `backend/internal/ports/system.go`

**Interfaces:**
- Produces: `AgentInstallJobStore` with `UpsertAgentInstallJob`, `GetAgentInstallJob`, `ListAgentInstallJobs`, and `InterruptActiveAgentInstallJobs`
- Job statuses: `installing`, `verifying`, `succeeded`, `failed`, `unsupported`, `interrupted`

- [ ] Write store tests proving upsert/get/list round trips diagnostics and timestamps, and startup recovery changes only `installing`/`verifying` jobs to `interrupted`.
- [ ] Run the focused store tests and confirm they fail because the schema and store do not exist.
- [ ] Add migration 0119, sqlc queries, the narrow port, and the store adapter; do not add unrelated CDC events.
- [ ] Run `npm run sqlc`, rerun the focused tests, and confirm they pass.
- [ ] Commit as `feat: persist harness install jobs`.

### Task 3: Replace unsafe recipes with capability-aware methods

**Files:**
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`
- Modify: `backend/internal/ports/system.go`
- Modify: `backend/internal/adapters/systemexec/systemexec.go`
- Modify: corresponding systemexec tests

**Interfaces:**
- Produces: stable `InstallMethod.ID`, `Recommended`, `ExpectedDestination`, and viability diagnostics
- Produces: a command execution request that explicitly closes stdin and carries a controlled environment

- [ ] Add table tests proving official script recipes use the bounded downloader, Vibe prefers `uv tool` then `pipx`, npm requires a writable prefix, unsupported methods report why, and no executable argv contains `sudo`, `-c`, or a shell pipeline.
- [ ] Add adapter tests proving installer commands receive closed stdin, bounded context, and noninteractive environment.
- [ ] Run the focused tests and confirm failures against the old first-available and direct `curl | shell` behavior.
- [ ] Implement explicit viable-method discovery, remove raw pip, and route official scripts through the bounded download-to-file runner.
- [ ] Implement the narrow execution request without changing unrelated command runners.
- [ ] Rerun the focused tests and commit as `fix: make harness install methods capability aware`.

### Task 4: Add adapter-backed verification and safe lifecycle transitions

**Files:**
- Create: `backend/internal/service/systeminstall/verifier.go`
- Create: `backend/internal/service/systeminstall/verifier_test.go`
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/systeminstall_test.go`
- Modify: `backend/internal/daemon/daemon.go`

**Interfaces:**
- Consumes: `ports.AgentResolver`, `ports.AgentBinaryResolver`, `AgentInstallJobStore`, `ports.CommandRunner`
- Produces: `Verify(ctx, harness) (resolvedPath string, output string, err error)` and durable state transitions

- [ ] Write verifier tests proving it uses the selected adapter's exact resolved path, performs a bounded version probe, and never invokes auth checking.
- [ ] Write service tests for `installing -> verifying -> succeeded`, verification failure, persistence failure, same-harness conflict, different-harness concurrency, and explicit `Verify` from failed/interrupted state.
- [ ] Run tests and confirm they fail before the verifier and transitions exist.
- [ ] Implement the verifier and inject the canonical agent resolver, job store, and runner through daemon wiring.
- [ ] Replace in-memory success with persisted transitions and make startup mark unfinished jobs interrupted.
- [ ] Rerun focused tests and commit as `fix: verify harness installs in the daemon`.

### Task 5: Block Droid installs while Droid sessions are active

**Files:**
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/systeminstall_test.go`
- Modify: `backend/internal/daemon/daemon.go`

**Interfaces:**
- Consumes: a narrow `SessionLister` exposing `ListAllSessions(context.Context)`
- Produces: typed `ErrHarnessActive` for non-terminated Droid sessions

- [ ] Add service tests for active Droid rejection, terminated Droid allowance, and non-Droid installs remaining unaffected.
- [ ] Run them and confirm the active-session case fails.
- [ ] Inject the session lister and reject before spawning any Droid installer process.
- [ ] Rerun tests and commit as `fix: protect active droid sessions during install`.

### Task 6: Expose methods, jobs, verification, and diagnostics over HTTP

**Files:**
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/controllers/systeminstall.go`
- Modify: `backend/internal/httpd/controllers/systeminstall_test.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`
- Regenerate: `backend/internal/httpd/apispec/openapi.yaml`
- Regenerate: `frontend/src/api/schema.ts`

**Interfaces:**
- Produces: `GET /api/v1/agents/install-jobs`, explicit-method install requests, and `POST /api/v1/agents/{agent}/verify`

- [ ] Write controller tests for method discovery, invalid method, batch hydration, verify, concurrent conflict, active-Droid conflict, diagnostics, and request-ID-preserving error envelopes.
- [ ] Run focused controller tests and confirm new routes/fields fail.
- [ ] Add DTOs and handlers, retaining the single-job GET for compatibility.
- [ ] Register every operation/schema and run `npm run api`.
- [ ] Run `go test ./internal/httpd/...` from `backend` and commit as `feat: expose recoverable harness install jobs`.

### Task 7: Make Settings recoverable and diagnostic

**Files:**
- Modify: `frontend/src/renderer/components/settings/HarnessSettingsSection.tsx`
- Modify: `frontend/src/renderer/components/settings/HarnessSettingsSection.test.tsx`
- Modify: `frontend/src/renderer/lib/api-client.ts`
- Modify: `frontend/src/renderer/i18n/*.json`

**Interfaces:**
- Consumes: generated installer method/job DTOs and install/verify endpoints
- Produces: hydrated job UI, method selection, visible polling errors, expandable/copyable diagnostics, `Verify again`, and `Reinstall`

- [ ] Write component tests for initial batch hydration, continued polling after remount, method selection, polling error display, interrupted state, diagnostics expansion/copy, and separate verify/reinstall calls.
- [ ] Run the component test and confirm failures against local-only state and client-side verification.
- [ ] Replace local success inference with daemon job rendering and poll whenever hydrated jobs are active.
- [ ] Add method choice and diagnostic recovery controls; remove the React agent-probe effect.
- [ ] Update translations for every existing locale and rerun component tests plus `npm run typecheck` in `frontend`.
- [ ] Commit as `fix: recover harness installs in settings`.

### Task 8: Verify PR #4221 end to end

**Files:**
- Review all files changed against `untrivial/main`

**Interfaces:**
- Produces: a pushed, conflict-free PR #4221 and a locally demonstrated native app

- [ ] Run `git diff --check`, focused Go tests, `cd backend && go test ./...`, `npm run frontend:typecheck`, and `cd frontend && npm run build`.
- [ ] Confirm API/sqlc regeneration leaves no drift and inspect the final diff for unrelated main regressions or generated-artifact mismatches.
- [ ] Push the PR branch and confirm GitHub reports PR #4221 mergeable with checks started.
- [ ] Launch the native AO Electron app using the repository's desktop-development workflow and verify the Harness Settings page renders.
- [ ] Report the exact verification results and any remaining external CI state.
