# Agent-Owned Model Catalogs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace AO-owned Claude Code, Muse, and Codex model lists with catalogs discovered from each installed agent, while preserving Amp's static mode list.

**Architecture:** Claude Code uses the Claude Agent SDK's structured `supportedModels()` method without yielding a prompt, Muse uses an isolated short-lived PTY to open `/model`, and Codex uses its structured app-server `model/list` method. Existing per-agent/per-project cache behavior remains authoritative, with asynchronous startup refresh for previously cached Claude Code and Muse scopes and the existing six-hour lazy refresh for all catalogs.

**Tech Stack:** Go, Claude Agent SDK, Node.js, `github.com/creack/pty`, Codex app-server JSON-RPC, SQLite/sqlc, existing agent model catalog service.

**Spec:** `docs/superpowers/specs/2026-08-29-agent-owned-model-catalogs-design.md`

## Constraints

- Do not add frontend or HTTP API shapes.
- Do not add or alter SQLite migrations.
- Do not hardcode Claude Code, Muse, or Codex model IDs.
- Do not change Amp's `low`, `medium`, `high`, and `ultra` modes.
- Preserve the last good cache on every discovery failure.
- Do not answer trust/auth prompts or repair/kill native agent state.

---

### Task 1: Remove AO-owned model IDs

**Files:**
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog_test.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`

1. Change the base-catalog test to require empty discoverable catalogs for Claude Code, Muse, and Codex, while Amp remains the only static mode list.
2. Run `cd backend && go test ./internal/adapters/agent/modelcatalog -run 'TestBase'` and confirm the old hardcoded catalogs fail the test.
3. Remove the three hardcoded model lists, classify them as discoverable catalog sources, and retain manual fallback behavior when discovery fails without cache.
4. Re-run the targeted test.

### Task 2: Add Claude SDK and isolated Muse terminal discovery

**Files:**
- Create: `backend/internal/adapters/agent/modelcatalog/terminal.go`
- Create: `backend/internal/adapters/agent/modelcatalog/terminal_test.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`
- Modify: `backend/internal/daemon/daemon.go`

1. Add failing SDK-runner tests for structured Claude model values, labels, version validation, timeout/close behavior, and fixture-driven Muse tests for ANSI/control stripping, complete-menu detection, IDs, incomplete output, and auth/trust output.
2. Run the new targeted tests and confirm they fail before implementation.
3. Add an injected Claude SDK catalog function and a terminal spawner interface backed in production by `runtime/ptyexec.Spawn` for Muse only.
4. Package a bounded Node helper that calls `supportedModels()` with persistence, settings, hooks, tools, and MCP disabled, then always closes the SDK query.
5. Implement Muse's bounded interaction: launch in project cwd/env, wait for a safe empty composer, write `/model\r`, capture a stable numbered menu, and always close the PTY.
6. Wire these paths into `Discoverer.Discover`; no AO session, transcript, provider prompt, trust answer, or auth answer is created.
7. Run `cd backend && go test ./internal/adapters/agent/modelcatalog`.

### Task 3: Discover Codex models through app-server

**Files:**
- Modify: `backend/internal/adapters/chatdriver/codexappserver/driver.go`
- Modify: `backend/internal/adapters/chatdriver/codexappserver/conversation.go`
- Modify: relevant `backend/internal/adapters/chatdriver/codexappserver/*_test.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog_test.go`
- Modify: `backend/internal/daemon/daemon.go`

1. Add failing tests that the Codex app-server driver exposes normalized visible `model/list` entries without opening a thread, preserving provider IDs, display names, and defaults.
2. Extract/reuse the existing `model/list` normalization shared by conversations and discovery.
3. Add a read-only driver discovery method and an injected Codex catalog source for `modelcatalog.Discoverer`.
4. Make Codex discovery fail safely when app-server/model-list is unavailable; never fall back to an AO-owned model list.
5. Run the targeted Codex and modelcatalog tests.

### Task 4: List cached scopes and refresh them at startup

**Files:**
- Modify: `backend/internal/ports/agent.go`
- Modify: `backend/internal/storage/sqlite/queries/agent_models.sql`
- Regenerate: `backend/internal/storage/sqlite/gen/*`
- Modify: `backend/internal/storage/sqlite/store/agent_model_catalog_store.go`
- Modify: relevant storage tests
- Modify: `backend/internal/service/agent/service.go`
- Modify: `backend/internal/service/agent/*_test.go`

1. Add failing store tests for listing cached scopes by agent and service tests for sequential background refresh, cached-scope-only behavior, ten-minute successful-validation suppression, and failure preservation.
2. Add `ListAgentModelCatalogsByAgent` to the cache port and SQL query source, then run `npm run sqlc` rather than editing generated code.
3. Implement the store mapping.
4. Add a non-blocking service warmer for only previously cached Claude Code and Muse scopes. Reuse the existing forced revalidation/coalescing path, and skip a scope only when its last successful validation was less than ten minutes ago.
5. Run targeted storage and service tests.

### Task 5: Wire daemon startup and verify unchanged API/UI behavior

**Files:**
- Modify: `backend/internal/daemon/daemon.go`
- Modify: relevant daemon tests

1. Add a failing daemon/service wiring test proving construction does not block and the warm pass is scheduled after dependencies exist.
2. Wire the Claude SDK source, Muse PTY spawner, Codex app-server source, and asynchronous Claude/Muse cache warmer.
3. Confirm uncached scopes are discovered on first picker access, cached catalogs are returned immediately, the existing six-hour `RefreshRecommended` behavior remains, and manual refresh still forces discovery.
4. Run focused tests, then:

```bash
cd backend && go test ./...
npm run frontend:typecheck
git diff --check
```

5. Review the final diff and keep only changes required by this plan.
