# OpenCode 2 Harness Implementation Plan

> **Status:** Implemented in the current `opencode-v2` adapters, reviewer, and
> storage migration. This file preserves the original rollout plan; its
> unchecked tasks do not mean the harness is absent. See [the v2 agent adapter](../../../backend/internal/adapters/agent/opencodev2/opencodev2.go)
> and [the migration](../../../backend/internal/storage/sqlite/migrations/0167_allow_opencode_v2_harness.sql).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `opencode-v2` as a distinct AO worker, Chat, and reviewer harness while keeping saved OpenCode 1 sessions bound to v1.

**Architecture:** Add a distinct domain identity and v2 adapters behind AO's existing agent, reviewer, and ACP ports. Share only binary discovery with v1, reject the wrong installed major version before launch, and use a v2 plugin/config overlay for TUI activity and permissions. Persist the new identity through a new migration and expose it through existing API and frontend contracts.

**Tech Stack:** Go, SQLite/goose/sqlc, OpenAPI generation, Electron/React/TypeScript, OpenCode 2 CLI and `@opencode/plugin`.

**Spec:** [OpenCode 2 harness design](../specs/2026-09-29-opencode-v2-harness-design.md)

## Global Constraints

- The harness and reviewer identifier is exactly `opencode-v2`; existing `opencode` remains OpenCode 1.
- The official OpenCode 2 and OpenCode 1 executables are both named `opencode` by default; require the selected major version before launch or restore.
- Preserve user OpenCode config, credentials, and native conversations. Never migrate them in AO.
- Keep CLI behavior behind daemon HTTP routes; keep Chat behind ACP and the detached provider host.
- Add a new SQLite migration; do not alter merged migrations or hand-edit sqlc output.
- Regenerate `openapi.yaml` and `frontend/src/api/schema.ts` together after DTO edits.
- Keep AO's primary listener on `127.0.0.1` and all AO-owned state under `~/.ao` or its configured override.
- Do not run a real package publish or production deployment as validation.

## File structure

- `backend/internal/adapters/agent/opencode/version.go`: common binary discovery plus bounded major-version verification, used by v1 and v2.
- `backend/internal/adapters/agent/opencodev2/`: v2 TUI launch, config, managed hook installer, activity mapping, and embedded v2 plugin; v1 package stays separate.
- `backend/internal/adapters/chatdriver/opencodev2acp/`: v2-specific ACP overlay and options, using AO's existing ACP transport.
- `backend/internal/adapters/reviewer/opencodev2/`: v2 read-only reviewer policy and restore.
- Existing domain, registry, storage, DTO, CLI, install, model catalog, frontend, and generated files receive only the new identity and compatibility mappings.

## Review Focus

1. A v1 binary on `PATH` for `opencode-v2` (or v2 for `opencode`) must produce a wrong-major error before a runtime starts; Task 2 tests both directions.
2. A prompt beginning with `-` must arrive as prompt text, not CLI help; Task 2 tests the argv and Task 7 checks the real CLI.
3. A user-owned `.opencode/plugins/ao-activity*.ts` must survive installation and cleanup; Task 3 tests collision and ownership.
4. A busy or unresponsive `ao hooks` must neither hang indefinitely nor crash the v2 plugin; Task 3 tests bounded failure handling.
5. A v2 reviewer must deny edits and unapproved shell commands even when project config grants them; Task 5 tests policy precedence and Task 7 checks the real CLI.

---

### Task 1: Durable harness identity and wire contract

**Files:** Modify `backend/internal/domain/harness.go`, `backend/internal/domain/reviewerharness.go`, `backend/internal/domain/session.go`, `backend/internal/httpd/controllers/dto.go`, `backend/internal/cli/spawn.go`, `backend/internal/skillassets/using-ao/commands/spawn.md`; create `backend/internal/storage/sqlite/migrations/0166_allow_opencode_v2_harness.sql`; regenerate `backend/internal/httpd/apispec/openapi.yaml`, `frontend/src/api/schema.ts`. Test in adjacent domain, storage, controller, CLI, and spec tests.

**Interfaces:** Produces `domain.HarnessOpenCodeV2` and `domain.ReviewerOpenCodeV2`, both with string value `opencode-v2`; other tasks consume those constants.

- [ ] Write tests proving both identities are known, a session with `harness=opencode-v2` round-trips SQLite, v1 rows stay `opencode`, DTO/spec enums expose the new values, and invalid harness input keeps the existing usage/API error envelope.
- [ ] Run the focused domain, storage, CLI, and HTTP tests; confirm failure on missing identity/constraint.
- [ ] Add constants and supported values; add a new surgical migration matching the current sessions CHECK, including its `qm` variant and reverse path. Change only source DTOs and run `npm run api`; run `npm run sqlc` only if query/schema sources change.
- [ ] Rerun focused tests and `git diff --check`; expect passes and generated spec/types in sync.
- [ ] Commit as `feat: add OpenCode 2 harness identity`.

### Task 2: Version-gated v2 TUI launch and restore

**Files:** Create `backend/internal/adapters/agent/opencode/version.go`, `backend/internal/adapters/agent/opencode/version_test.go`, `backend/internal/adapters/agent/opencodev2/opencodev2.go`, `backend/internal/adapters/agent/opencodev2/opencodev2_test.go`; modify `backend/internal/adapters/agent/opencode/opencode.go`, `backend/internal/adapters/agent/registry/registry.go` and registry tests.

**Interfaces:** `opencode.ResolveBinaryForMajor(ctx context.Context, major int) (string, error)` resolves and probes one path; `opencodev2.New() *Plugin` implements `ports.Agent` and the auth/signaling ports used by the registry. Keep the resolved path within one launch/restore attempt.

- [ ] Write fake-executable tests for version `1.x`, `2.x`, malformed output, timeout, and missing binary; test v1/v2 cross-major rejection before launch. Add table tests for fresh and native-ID restore, model, permissions, AO prompt overlay, `--standalone`, and leading-dash prompt delivery.
- [ ] Run `go test ./internal/adapters/agent/opencode ./internal/adapters/agent/opencodev2 ./internal/adapters/agent/registry -count=1` from `backend`; confirm the new cases fail.
- [ ] Implement the bounded version probe and v2 adapter using the actual v2 CLI flags confirmed by `opencode --help` or official v2 source. Express v2 standing instructions as `agents.<ao-id>.system` and ordered `permissions`; preserve `OPENCODE_CONFIG_CONTENT` owned by callers. Register v2 without changing v1's manifest ID.
- [ ] Rerun the focused packages; verify wrong-major errors, cancellation, and restore do not create side effects.
- [ ] Commit as `feat: launch OpenCode 2 TUI sessions`.

### Task 3: V2 activity and managed workspace hooks

**Files:** Create `backend/internal/adapters/agent/opencodev2/hooks.go`, `activity.go`, `assets/ao-activity.ts` and adjacent tests; modify `backend/internal/adapters/agent/opencode/hooks.go`, `backend/internal/adapters/agent/activitydispatch/dispatch.go` and tests.

**Interfaces:** `opencodev2.Plugin.GetAgentHooks/UninstallHooks/AreHooksInstalled` manage the v2 plugin and skill; `opencodev2.DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)` maps v2 reports. The embedded plugin calls `ao hooks opencode-v2 <event>` with native session ID and `AO_RUNTIME_LAUNCH_ID`.

- [ ] Write tests for install/reinstall/uninstall ownership, v1-to-v2 and v2-to-v1 managed plugin cleanup, preservation of foreign files, gitignored hook footprint, five activity states, generation ID forwarding, and bounded missing/failing hook behavior.
- [ ] Run focused hook, activitydispatch, and agent registry tests; confirm failure.
- [ ] Implement `@opencode/plugin` default export using v2 events/hooks; enforce ordered bounded best-effort hook delivery. Install only AO-owned artifacts, remove only the other major's marker-verified AO plugin, and leave the user skill and other plugins intact.
- [ ] Rerun focused tests; use a v2 plugin typecheck or live plugin load when an isolated v2 CLI is available.
- [ ] Commit as `feat: report OpenCode 2 TUI activity`.

### Task 4: OpenCode 2 ACP Chat

**Files:** Create `backend/internal/adapters/chatdriver/opencodev2acp/driver.go` and tests; modify `backend/internal/adapters/chatdriver/registry/registry.go` and tests; extend v2 config helpers in `backend/internal/adapters/agent/opencodev2/`.

**Interfaces:** `opencodev2acp.New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver`; v2 helper `PrepareACPConfigContent(existing, systemPrompt string, permissions ports.PermissionMode) (string, error)` supplies `agents` and v2 permission rules.

- [ ] Write tests for registry selection, `opencode acp` launch through the v2 binary, v1/v2 wrong-major rejection, provider/model validation, model-before-effort ordering, mode switching, default/accept-edits/auto/bypass approval, and refusal to resume a conversation under the other harness identity.
- [ ] Run `go test ./internal/adapters/chatdriver/{opencodev2acp,registry,acp,persistenthost} -count=1`; confirm the v2 tests fail.
- [ ] Implement the v2 ACP config/options and register the driver through the detached host, reusing nativeacp transport and its permission reply mechanics. Do not call OpenCode's v1 server API.
- [ ] Rerun focused tests and existing v1 ACP tests; expect both major identities to remain distinct.
- [ ] Commit as `feat: support OpenCode 2 Chat over ACP`.

### Task 5: Read-only OpenCode 2 reviewer

**Files:** Create `backend/internal/adapters/reviewer/opencodev2/opencodev2.go` and tests; modify `backend/internal/adapters/reviewer/registry.go` and tests. Task 1 owns reviewer identity sources.

**Interfaces:** `opencodev2.New() *Reviewer` implements `ports.Reviewer`, `ports.ReviewerCanceller`, and `ports.ReviewerRestorer`; `Harness() domain.ReviewerHarness` returns `ReviewerOpenCodeV2`.

- [ ] Write tests that assert a final agent-level `*/*/deny` followed by `read`, `glob`, and `grep` allows; an external-directory allow limited to the AO task prompt root; and shell allows only `gh api *`, `git diff *`, `git log *`, `git show *`, `git status *`, `ao review submit *`, `printf * | gh api *`, and `printf * | ao review submit *`. Test edit denial despite permissive project config or saved approvals, restore policy, cancellation, and wrong-major errors.
- [ ] Run `go test ./internal/adapters/reviewer/... -count=1`; confirm new assertions fail.
- [ ] Implement the reviewer adapter and register it only after its policy tests pass. Keep the policy in the reviewer process's v2 overlay, not in shared user config.
- [ ] Rerun the reviewer suite; confirm v1 reviewer behavior is unchanged.
- [ ] Commit as `feat: add OpenCode 2 reviewer`.

### Task 6: Discovery, install, and supervisor selection

**Files:** Modify `backend/internal/cli/doctor.go`, `backend/internal/adapters/agent/modelcatalog/catalog.go`, `backend/internal/service/systeminstall/{systeminstall.go,agentplans.go}`, `backend/internal/service/agentauth/plans.go`, applicable install DTOs, `packages/product-ui/src/agents.ts`, `frontend/src/renderer/{lib/agent-select-options.ts,lib/reviewer-harnesses.ts,components/AgentAvatar.tsx,components/InstallDependencyDialog.tsx,components/TerminalPane.tsx,components/ReviewerSelect.tsx}` and related tests/translations. Regenerate API types if DTOs change.

**Interfaces:** Discovery exposes `opencode-v2` separately with shared executable name but major-aware readiness; install target `opencode-v2` proposes official v2 packages and warns before replacing a default v1 install.

- [ ] Write tests for v1/v2 doctor readiness on one installed binary, v2 model list parsing, correct install choices and replacement notice, distinct agent/reviewer labels, icon and terminal behavior, and UI selection.
- [ ] Run focused Go and frontend tests; confirm failure on missing v2 mappings.
- [ ] Add the mappings without broad frontend refactoring; keep cloud-specific provider lists unchanged because this plan targets the local AO harness and does not add a cloud provider contract. Run `npm run api` for the install DTO change.
- [ ] Rerun focused tests plus `npm run frontend:typecheck`; expect no missing enum/label keys.
- [ ] Commit as `feat: expose OpenCode 2 in AO setup and UI`.

### Task 7: Integration and complete validation

**Files:** Extend opt-in live tests under `backend/internal/adapters/agent/opencodev2/` and `backend/internal/adapters/chatdriver/opencodev2acp/`; update `docs/STATUS.md` and user-facing harness docs only for verified capability.

**Interfaces:** No new production interface; verify the complete v2 path from selection through restore.

- [ ] Obtain a pinned official v2 executable in a scratch location without replacing the user's v1 installation; run a live TUI and ACP probe with scratch AO data. Capture exact version, flags, plugin load, prompt text, native ID, idle/blocking signals, restore, and reviewer policy results. If installation or credentials are unavailable, record the precise gap and do not claim live success.
- [ ] Add opt-in live regression coverage for the confirmed behavior and run it against that binary; include a leading-dash prompt and denied reviewer edit/shell attempt.
- [ ] Run `cd backend && go build ./... && go test ./... && go test -race ./... && go vet ./...`, `npm run lint`, `npm run frontend:typecheck`, `cd frontend && npm run build`, `npm run api`, and `npm run sqlc` if storage source changed; run the relevant local CI workflow commands where Docker/runtime access exists.
- [ ] Review `git diff --check`, generated drift, old OpenCode 1 tests, and the final branch against the spec; fix failures and rerun affected full suites.
- [ ] Commit verified documentation/test updates as `test: verify OpenCode 2 harness integration`; report any unavailable local CI job exactly and confirm it through remote CI before PR handoff.
