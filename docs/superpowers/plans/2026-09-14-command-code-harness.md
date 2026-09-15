# Command Code (cmd) Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Command Code (`command-code`, binary `cmd`, Windows alias `cmdc`) as a first-class AO agent harness (issue #5394).

**Architecture:** A new `ports.Agent` adapter (`backend/internal/adapters/agent/commandcode`) that launches Command Code's interactive TUI and injects the initial task after startup, mirroring the Grok adapter. AO enumerates harnesses through `domain.AllHarnesses` + the adapter registry, so the remaining work is registering the new id in the domain constant set, registry, model catalog, installer plans, auth plans, API enum tags, docs, and tests.

**Tech Stack:** Go (Cobra/Huma daemon), sqlc/SQLite (untouched), Electron/React frontend (generated types only), Command Code CLI 1.54+.

---

## Verified Command Code facts (source: commandcode.ai docs)

- Binary: `cmd` on Unix; Windows alias `cmdc` (`cmd` is the built-in shell); full name `command-code` works everywhere.
- Interactive: `cmd` starts the TUI; `cmd "message"` starts with an initial message.
- Headless: `-p, --print [query]`, `--output-format text|json`. Single turn; not used by AO (supervised sessions are multi-turn).
- Permissions: `--permission-mode default|plan|auto-accept|dont-ask` (legacy `standard`); `--yolo` (alias `--dangerously-skip-permissions`); `--auto-accept` alias.
- Model: `-m, --model <provider/model>`; `--effort <low|medium|high|…>`; `cmd --list-models`.
- Sessions: `-r/--resume [name]`, `-c/--continue`, `--session <path|id>`, `--fork-session`, `--no-session`.
- Automation hygiene: `--skip-onboarding`, `--no-auto-update`, `-t/--trust`.
- Auth: `cmd login` opens a browser (API key paste also works); `cmd status` shows authentication status.
- Install: npm package `command-code`.
- Hooks: `.commandcode/settings.json` (`PreToolUse`, `PostToolUse`, `Stop`, `SessionStart`, payload `.session_id`) — NOT wired in this change.
- Memory: `AGENTS.md` tiers (`~/.commandcode/AGENTS.md`, `<project>/AGENTS.md`, else `<project>/.commandcode/AGENTS.md`) feed the system prompt — no CLI system-prompt flag.

## Design decisions (intentional deviations, to document in the PR body)

1. **Interactive TUI, not headless.** `GetPromptDeliveryStrategy` returns `AfterStart`; AO injects the initial task once the TUI is ready. Command Code accepts a positional initial message, but injecting after startup means a prompt beginning with `-` is never parsed as a flag (the Grok adapter's rationale).
2. **No native restore id.** AO installs no Command Code hooks in this change, so no `.session_id` is captured; `GetRestoreCommand` reports `ok=false` and the session manager relaunches with AO's saved prompt. `cmd -r`/`-c`/`--session` remain available manually.
3. **No AO system-prompt injection.** Command Code exposes no system-prompt flag, and its `AGENTS.md` memory tiers are project-owned (root `AGENTS.md` takes precedence over `.commandcode/AGENTS.md`), so AO will not overwrite or shadow them.
4. **No `--effort` wiring.** `domain.AgentConfig` has no effort field (only model/mode/permissions), so there is no AO-side knob to map.
5. **No chat/ACP driver.** Command Code has no ACP surface, so it is a Terminal UI harness only (`ChatHarnesses` stays unchanged).
6. **Permission mapping.** Default → no flag; `accept-edits` and `auto` → `--permission-mode auto-accept`; `bypass-permissions` → `--yolo`.

## File structure

- Create `backend/internal/adapters/agent/commandcode/commandcode.go` — plugin, manifest, config spec, launch/restore, session info, binary resolution, prompt readiness.
- Create `backend/internal/adapters/agent/commandcode/auth.go` — `cmd status` probe with the "verified" correction.
- Create `backend/internal/adapters/agent/commandcode/commandcode_test.go`, `auth_test.go`.
- Modify `backend/internal/domain/harness.go` — `HarnessCommandCode`, `AllHarnesses`.
- Modify `backend/internal/adapters/agent/registry/registry.go` + `registry_test.go`.
- Modify `backend/internal/adapters/agent/modelcatalog/catalog.go` + `catalog_test.go`.
- Modify `backend/internal/service/systeminstall/systeminstall.go` + `agentplans.go` + `agentplans_test.go`.
- Modify `backend/internal/service/agentauth/plans.go` + `plans_test.go`.
- Modify `backend/internal/httpd/controllers/dto.go`; regenerate `backend/internal/httpd/apispec/openapi.yaml` + `frontend/src/api/schema.ts` (`npm run api`).
- Create `docs/harnesses/command-code.md`.

## Tasks

### Task 1: Domain harness id
- [ ] Add `HarnessCommandCode AgentHarness = "command-code"` after `HarnessFake`'s block; append `HarnessCommandCode` to `AllHarnesses`.
- [ ] Run `cd backend && go test ./internal/domain/...`.

### Task 2: Adapter package (TDD)
- [ ] Write `commandcode_test.go` covering: manifest; model config spec; `PromptDeliveryAfterStart`; `PromptReadinessHints`; launch argv for default/accept-edits/auto/bypass permissions + model forwarding + no prompt in argv; restore with metadata id → `--resume <id>`; restore without id → `ok=false`; session info from metadata.
- [ ] Write `auth_test.go` covering: "Authentication verified" → authorized; "Authentication required" → unauthorized; empty/garbage → unknown; binary-missing → error.
- [ ] Implement `commandcode.go` + `auth.go` until tests pass.

### Task 3: Registry
- [ ] Register `commandcode.New()` in `Constructors()`.
- [ ] Add `TestRegistryIncludesCommandCode`; confirm the mandatory auth/model capability tests pass.

### Task 4: Model catalog
- [ ] Add `"command-code": {args: []string{"--no-auto-update", "--list-models"}, parser: parseIDLines}`.
- [ ] Add a `commandSpecs` assertion to `catalog_test.go`.

### Task 5: Installer plan
- [ ] Add `TargetCommandCode` + `agentTargets` entry + docs URL + `planNPM(target, "command-code")` case.
- [ ] Update `agentplans_test.go` target count (27 → 28) and any per-target expectations.

### Task 6: Auth plan
- [ ] Add `plan("command-code", ActionLogin, "Log in to Command Code", []string{"cmd", "login"}, …)`.
- [ ] Add the matching case to `plans_test.go`.

### Task 7: API contract
- [ ] Add `command-code` to the `domain.AgentHarness` enum tags in `dto.go`.
- [ ] Run `npm run api`; commit `openapi.yaml` and `schema.ts` together.

### Task 8: Docs
- [ ] `docs/harnesses/command-code.md` mirroring `omp.md`: Install, Supported AO Mode, Activity Tracking, Chat Mode, Restore, Auth, Not Supported.

### Task 9: Verification
- [ ] `npm run lint`, `cd backend && go build ./... && go test ./... && go vet ./...`, `npm run frontend:typecheck`.
- [ ] Open the PR closing #5394 with the pr-description header counts.
