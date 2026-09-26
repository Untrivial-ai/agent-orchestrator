# Cloud PR Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reuse the existing Reviews experience for cloud sessions on Docker, NodeOps, and Coder through one control-plane/worker implementation, with Claude, Cursor, and Codex reviewer selection and in-sandbox installation when a supported CLI is missing.

**Architecture:** PostgreSQL stores cloud session preferences and review-run history. Authenticated cloud handlers validate intent and enqueue typed worker transport requests. The worker owns harness inspection/installation and reviewer processes; output continues through the existing ticketed terminal mux. Shared React presentation branches only at data-access and terminal-transport boundaries, leaving local daemon behavior unchanged.

**Tech Stack:** Go, PostgreSQL/goose, chi, worker transport WebSocket/RPC, Electron/React, TanStack Query, TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-20-cloud-pr-review-design.md`

## Constraints

- Do not modify local backend review behavior or local API contracts.
- Cloud reviewers are exactly `claude-code`, `cursor`, and `codex`.
- Empty reviewer override resolves to the current worker harness.
- Renderer requests never contain install commands, package names, URLs, or argv.
- Review and install execution stays above sandbox-provider adapters.
- Update session scan destinations and tests atomically with database fields.
- Failed/cancelled reviews are retryable for the same SHA; concurrent running reviews are not duplicated.

---

### Task 1: Persist cloud review preferences and retryable run metadata

**Files:**
- Create: next `cloud/internal/postgres/migrations/*.sql`
- Modify: `cloud/internal/domain/types.go`
- Modify: `cloud/internal/domain/pull_request.go`
- Modify: `cloud/internal/postgres/project_session_store.go`
- Modify: `cloud/internal/postgres/sandbox_store.go`
- Modify: `cloud/internal/postgres/review_run_store.go`
- Test: corresponding PostgreSQL/store tests

- [ ] Add failing tests for session scan/defaults and run retry semantics.
- [ ] Add additive session preference and review metadata columns.
- [ ] Replace permanent SHA uniqueness with running-only uniqueness.
- [ ] Thread fields through every session scan and response conversion.
- [ ] Run focused postgres tests.

### Task 2: Add cloud session preference API

**Files:**
- Create: `cloud/internal/httpapi/session_preferences_handlers.go`
- Modify: `cloud/internal/httpapi/server.go`
- Modify: `cloud/internal/httpapi/resource_handlers.go`
- Test: cloud HTTP handler tests

- [ ] Write failing authorization, validation, and response tests.
- [ ] Add typed PATCH handling for review/feedback/merge preferences.
- [ ] Validate reviewer values against the three-item cloud allowlist.
- [ ] Return preferences in session list/detail responses.
- [ ] Run focused handler tests.

### Task 3: Implement manual review lifecycle

**Files:**
- Modify: `cloud/internal/httpapi/pull_request_handlers.go`
- Modify: `cloud/internal/httpapi/server.go`
- Modify: `cloud/internal/postgres/review_run_store.go`
- Modify: `cloud/internal/githubapp/review.go`
- Modify: `cloud/internal/githubapp/service.go`
- Test: handler, store, and GitHub service tests

- [ ] Write failing tests for trigger, duplicate trigger, cancel, retry, and authorization.
- [ ] Resolve the effective reviewer and credential before run creation.
- [ ] Persist one run per eligible PR and launch its dedicated reviewer terminal.
- [ ] Atomically cancel running runs and close terminals.
- [ ] Return UI-compatible state/history and stable notices/errors.
- [ ] Run focused review lifecycle tests.

### Task 4: Carry reviewer identity and credentials through terminal transport

**Files:**
- Modify: `cloud/internal/worker/protocol.go`
- Modify: `cloud/internal/postgres/worker_transport_store.go`
- Modify: `cloud/internal/workertransport/supervisor.go`
- Modify: `cloud/cmd/ao-worker/main.go`
- Test: transport store, supervisor, and worker tests

- [ ] Write failing tests proving reviewer terminal open carries the selected harness.
- [ ] Extend the typed terminal-open request without changing normal terminals.
- [ ] Resolve only the selected user's cloud credential into the reviewer process.
- [ ] Launch Claude, Cursor, or Codex with the established noninteractive review argv.
- [ ] Preserve terminal output, activity, input, close, and submission behavior.
- [ ] Run focused transport tests.

### Task 5: Add typed cloud harness inspect/install operations

**Files:**
- Modify: `cloud/internal/worker/protocol.go`
- Modify: `cloud/internal/postgres/worker_transport_store.go`
- Modify: `cloud/internal/workertransport/supervisor.go`
- Modify: `cloud/cmd/ao-worker/main.go`
- Create: worker harness installer source/tests as appropriate
- Modify: `cloud/internal/httpapi/server.go`
- Create/modify: cloud harness handlers/tests

- [ ] Write failing allowlist, inspection, install fencing, bounded-download, and verification tests.
- [ ] Add session-scoped inspect/install routes and transport requests.
- [ ] Install pinned Claude/Codex packages or the pinned Cursor archive into an AO-owned user tool directory.
- [ ] Verify the canonical binary before reporting ready.
- [ ] Ensure newly opened terminals receive the AO tool directory on PATH.
- [ ] Run focused worker and API tests.

### Task 6: Complete automatic review, feedback, and merge preferences

**Files:**
- Modify: cloud PR observer/service and store files selected by existing boundaries
- Test: observer/service tests

- [ ] Write failing tests for automatic review reuse, feedback idempotency, and one-shot termination intent.
- [ ] Route automatic review through the manual lifecycle with `trigger_source=auto`.
- [ ] Gate CI/review message injection with persisted preferences and durable observed-fact fencing.
- [ ] Request normal reconciler-owned teardown after merge when enabled.
- [ ] Run focused observer tests.

### Task 7: Extend the typed cloud frontend client

**Files:**
- Modify: `contracts/cloud/openapi.yaml`
- Modify: `packages/cloud-client/src/schema.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/types.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.ts`
- Test: cloud client tests

- [ ] Add failing client path/body/response tests.
- [ ] Define review, preference, reviewer availability, and install contracts.
- [ ] Implement typed client methods.
- [ ] Regenerate/check cloud schema artifacts using repository commands.

### Task 8: Route the existing Reviews UI to cloud

**Files:**
- Modify: `frontend/src/renderer/lib/session-reviews.ts`
- Modify: `frontend/src/renderer/components/SessionInspector.tsx`
- Modify: `frontend/src/renderer/components/ReviewerSelect.tsx`
- Modify: `frontend/src/renderer/hooks/useWorkspaceQuery.ts`
- Test: corresponding Vitest suites

- [ ] Write failing tests proving cloud sessions never call local review/preference routes.
- [ ] Adapt cloud review responses to the existing shared view model.
- [ ] Limit cloud selector options to Claude, Cursor, and Codex.
- [ ] Show credential setup or Install according to independent readiness states.
- [ ] Preserve every existing local UI test and request path.

### Task 9: Connect the cloud reviewer terminal

**Files:**
- Modify: `frontend/src/renderer/components/SessionView.tsx`
- Modify: `frontend/src/renderer/components/TerminalPane.tsx`
- Modify: `frontend/src/renderer/lib/cloud-terminal-mux.ts`
- Modify: terminal target types if required
- Test: SessionView, TerminalPane, and cloud mux tests

- [ ] Write failing tests for reviewer target discovery and cloud ticket connection.
- [ ] Carry cloud org/session/reviewer terminal identity through the target.
- [ ] Reuse cloud ticket/mux behavior and avoid local terminal routes.
- [ ] Remove the tab when the review terminal is no longer available.

### Task 10: Verify provider-neutral behavior and repository health

- [ ] Run all focused tests from Tasks 1–9.
- [ ] Run `go test ./...` in `cloud` and required backend contract tests.
- [ ] Run frontend tests, `npm run frontend:typecheck`, and `npm run frontend:build`.
- [ ] Run cloud contract drift/image contract checks.
- [ ] Run `npm run lint`.
- [ ] Start local cloud Docker and frontend, then exercise reviewer selection/install, run/cancel/retry, terminal, results, and preferences.
- [ ] Report NodeOps/Coder live-provider verification gaps explicitly if credentials are unavailable.
