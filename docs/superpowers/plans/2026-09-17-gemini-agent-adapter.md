# Gemini CLI Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add independently shippable Gemini CLI worker/orchestrator, durable activity/restore, readiness, model configuration, and native ACP Chat support without replacing Gemini's built-in safety instructions.

**Architecture:** A new `gemini` TUI adapter follows the existing Qwen-shaped launch and hook boundaries but uses Gemini's native `--prompt-interactive`, caller-assigned `--session-id`, and exact `--resume`. A prompt builder exports the installed Gemini version's built-in prompt with `GEMINI_WRITE_SYSTEM_MD`, caches that default under AO data by executable fingerprint and permission mode, appends AO's standing instructions into a per-session AO-owned file, and exposes it through `GEMINI_SYSTEM_MD`; Chat reuses the same builder through a thin native ACP binding. Production registration is gated on live conformance against Gemini CLI 0.60.0 or newer.

**Tech Stack:** Go adapters and tests, Gemini CLI 0.60.0+, JSON matcher-group hooks, AO's shared ACP driver/persistent host, SQLite/goose, generated OpenAPI and TypeScript.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- Canonical harness ID is `gemini`; the same adapter serves workers and orchestrators.
- Minimum candidate version is Gemini CLI `0.60.0`; production registration is forbidden until Task 1 passes against that release on macOS/Linux and a supported Windows runner.
- `GEMINI_SYSTEM_MD` may point only at an AO-owned combined prompt containing the exact exported upstream default followed by AO instructions. It must never point directly at AO's role prompt.
- Cache exported defaults by executable fingerprint and effective approval mode; write combined prompts under `${AO_DATA_DIR}/prompts/<ao-session-id>/` with mode `0600`.
- Initial tasks use `--prompt-interactive`; never use headless `--prompt` or terminal keystroke injection.
- Restore always uses the stored explicit native ID; `--resume` without an ID is prohibited.
- User `.gemini/settings.json`, hooks, context files, OAuth/keychain data, and project `GEMINI.md` remain untouched except for additive AO-owned hook entries managed by `hooksjson.Manager`.
- AO `auto` permission mode maps conservatively to Gemini `auto_edit`; only `bypassPermissions` maps to `yolo`.
- Chat registration requires the Task 7 ACP conformance suite; TUI-to-Chat handoff remains excluded.
- Do not modify reviewer adapters, reviewer enums, reviewer UI, or reviewer execution.

---

### Task 1: Pin and prove the upstream Gemini contract

**Files:**
- Create: `backend/internal/adapters/agent/gemini/conformance_test.go`
- Create: `backend/internal/adapters/agent/gemini/testdata/contract-v0.60.0.json`
- Create: `backend/internal/adapters/chatdriver/geminiacp/live_test.go`

**Interfaces:**
- Consumes: installed `gemini`, `AO_LIVE_GEMINI=1`, and the user's existing Gemini authentication.
- Produces: an executable release gate proving `--prompt-interactive`, `--session-id`, exact `--resume`, hooks, prompt export, and ACP behavior for version `>=0.60.0`.

- [ ] **Step 1: Write the failing release-contract test**

Add a table-backed test which executes `gemini --version` and `gemini --help`, parses semantic version output, and requires these exact tokens for `>=0.60.0`:

```go
var requiredHelpTokens = []string{
    "--prompt-interactive", "--approval-mode", "--model",
    "--session-id", "--resume", "--acp", "--skip-trust",
}

func TestLiveGeminiReleaseContract(t *testing.T) {
    if os.Getenv("AO_LIVE_GEMINI") != "1" {
        t.Skip("set AO_LIVE_GEMINI=1 to test the installed Gemini CLI")
    }
    // Resolve the real binary, require version >= 0.60.0, then require every
    // token in requiredHelpTokens. Record stdout in contract-v0.60.0.json only
    // after every behavioral probe below passes.
}
```

The JSON fixture must contain `version`, `binarySHA256`, `platform`, `promptExport`, `sessionID`, `restore`, `hooks`, and `acp` fields; booleans are accepted only after their corresponding subprocess assertion succeeds.

- [ ] **Step 2: Add behavioral probes for prompt export, hooks, and restore**

Use a temporary project and temporary home. Start Gemini in a PTY with `GEMINI_WRITE_SYSTEM_MD=<temp>/default.md`, `--skip-trust`, `--session-id ao-gemini-contract`, `--approval-mode plan`, and `--prompt-interactive "Reply with contract-ok"`. Require the default prompt file to become non-empty within 15 seconds, terminate only that process, and assert the exported text does not contain the AO marker. Configure project hooks that append raw stdin to temporary files and prove `SessionStart`, `BeforeAgent`, `AfterAgent`, `Notification`, and `SessionEnd` carry the documented `session_id`; relaunch with `--resume ao-gemini-contract` and require `SessionStart.source == "resume"`.

- [ ] **Step 3: Add a live ACP probe without registering Chat**

Launch `gemini --acp`, initialize the protocol, create a session, send one prompt, cancel a second prompt, terminate/restart the process, load the first session by its exact provider ID, and require replay without duplicate message IDs. Trigger a harmless permission request in `default` mode and require an ACP permission RPC rather than terminal text.

- [ ] **Step 4: Run the gate and enforce its stop condition**

Run: `cd backend && AO_LIVE_GEMINI=1 go test ./internal/adapters/agent/gemini ./internal/adapters/chatdriver/geminiacp -run Live -count=1 -v`

Expected: PASS on each supported platform. If prompt export fails to preserve a non-empty default, exact-ID restore fails, a hook changes permission behavior, or ACP lacks load/permission/cancel/replay, stop: do not execute Tasks 2-9 and do not add Gemini to any production registry, migration, API enum, or UI list. Commit only the conformance test/fixture with a failing-capability report.

- [ ] **Step 5: Commit the proven contract**

```bash
git add backend/internal/adapters/agent/gemini/conformance_test.go \
  backend/internal/adapters/agent/gemini/testdata/contract-v0.60.0.json \
  backend/internal/adapters/chatdriver/geminiacp/live_test.go
git commit -m "test: pin Gemini CLI adapter contract"
```

### Task 2: Build the version-matched combined system prompt

**Files:**
- Create: `backend/internal/adapters/agent/gemini/prompt.go`
- Create: `backend/internal/adapters/agent/gemini/prompt_test.go`

**Interfaces:**
- Consumes: resolved Gemini binary, `ports.PermissionMode`, AO data directory, AO session ID, and AO `SystemPromptFile`.
- Produces: `type PromptBuilder interface { Prepare(context.Context, PromptRequest) (string, error) }`, `PromptRequest`, and `NewPromptBuilder() PromptBuilder`.

- [ ] **Step 1: Write failing prompt-builder tests**

Define the interface exactly:

```go
type PromptRequest struct {
    Binary, DataDir, SessionID, WorkspacePath, AOSystemPromptFile string
    Permissions ports.PermissionMode
}

type PromptBuilder interface {
    Prepare(context.Context, PromptRequest) (string, error)
}
```

Tests must prove: empty AO prompt returns an empty path; export runs with `GEMINI_SYSTEM_MD` removed and `GEMINI_WRITE_SYSTEM_MD` set; cache key changes with executable SHA-256 or approval mode; identical requests reuse the cached default; combined bytes equal `strings.TrimRight(default, "\n") + "\n\n" + strings.TrimLeft(ao, "\n")`; files are `0600`; concurrent calls produce one valid atomic result; missing/empty export and timeout fail closed.

- [ ] **Step 2: Run tests and observe the missing implementation**

Run: `cd backend && go test ./internal/adapters/agent/gemini -run Prompt -count=1`

Expected: FAIL because `PromptBuilder`, `PromptRequest`, and `NewPromptBuilder` do not exist.

- [ ] **Step 3: Implement the exporter and cache**

Implement an injectable process runner. The real runner starts Gemini with the same effective approval mode, `--skip-trust`, and a disposable caller-assigned session ID, waits up to 15 seconds for the export file to become non-empty, then terminates only that child. Store defaults at `agent-runtime/gemini/system-prompts/<binary-sha256>/<approval-mode>/default.md`; write combined output atomically to `prompts/<session-id>/gemini-system.md`. Never read or write `~/.gemini` during composition.

- [ ] **Step 4: Run prompt tests**

Run: `cd backend && go test ./internal/adapters/agent/gemini -run Prompt -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/gemini/prompt.go backend/internal/adapters/agent/gemini/prompt_test.go
git commit -m "feat: preserve Gemini system prompt for AO sessions"
```

### Task 3: Implement Gemini TUI launch, configuration, and restore

**Files:**
- Create: `backend/internal/adapters/agent/gemini/gemini.go`
- Create: `backend/internal/adapters/agent/gemini/gemini_test.go`
- Create: `backend/internal/adapters/agent/gemini/install.go`

**Interfaces:**
- Consumes: `ports.Agent`, `ports.AgentBinaryResolver`, and the Task 2 `PromptBuilder`.
- Produces: `gemini.New() *Plugin`, `ResolveGeminiBinary(context.Context)`, exact launch/restore argv, `AugmentRuntimeEnv(map[string]string, string)`, and standard session metadata.

- [ ] **Step 1: Write failing table-driven adapter tests**

Cover worker and orchestrator launches, promptless sessions, model trimming, all four AO permission modes, leading-dash prompts, caller-assigned native IDs, system-prompt builder failure, exact restore, missing native ID, and Windows binary candidates. Assert these command shapes:

```text
worker:       gemini --skip-trust --session-id <native> [--model M] [--approval-mode MODE] --prompt-interactive <task>
orchestrator: gemini --skip-trust --session-id <native> [--model M] [--approval-mode MODE]
restore:      gemini --skip-trust [--model M] [--approval-mode MODE] --resume <native>
```

Expected mappings are default→no flag, acceptEdits→`auto_edit`, auto→`auto_edit`, bypassPermissions→`yolo`. Assert `GetPromptDeliveryStrategy` always returns `ports.PromptDeliveryInCommand`.

- [ ] **Step 2: Run the failing adapter tests**

Run: `cd backend && go test ./internal/adapters/agent/gemini -run 'Launch|Restore|Config|Binary|SessionInfo' -count=1`

- [ ] **Step 3: Implement the minimal adapter**

Use a deterministic native ID derived from AO `NativeSessionID` when supplied, otherwise `SessionID`; validate it to Gemini's `[A-Za-z0-9_-]+` rule. `GetConfigSpec` exposes `model` and AO's permission enum. Both launch and restore call `PromptBuilder.Prepare`; `AugmentRuntimeEnv` derives `prompts/<AO_SESSION_ID>/gemini-system.md` from protected runtime env and sets `GEMINI_SYSTEM_MD` only when that regular AO-owned file exists. `SessionInfo` delegates to `agentbase.StandardSessionInfo`.

- [ ] **Step 4: Run adapter tests**

Run: `cd backend && go test ./internal/adapters/agent/gemini -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/gemini/gemini.go \
  backend/internal/adapters/agent/gemini/gemini_test.go \
  backend/internal/adapters/agent/gemini/install.go
git commit -m "feat: add Gemini CLI launch and restore adapter"
```

### Task 4: Install non-destructive Gemini activity hooks

**Files:**
- Create: `backend/internal/adapters/agent/gemini/hooks.go`
- Create: `backend/internal/adapters/agent/gemini/hooks_test.go`
- Create: `backend/internal/adapters/agent/gemini/activity.go`
- Create: `backend/internal/adapters/agent/gemini/activity_test.go`
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch.go`
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch_test.go`

**Interfaces:**
- Consumes: Gemini matcher-group hooks and `activitydispatch.DeriveFunc`.
- Produces: `gemini.DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)` and complete signal coverage for `gemini`.

- [ ] **Step 1: Write failing hook-preservation tests**

Require additive entries in `.gemini/settings.json` for `SessionStart(startup|resume|clear)`, `BeforeAgent`, `BeforeTool`, `AfterTool`, `AfterAgent`, `Notification`, and `SessionEnd`. Assert unrelated settings and user hooks survive byte-equivalent JSON values, repeated installation is idempotent, and uninstall removes only commands prefixed `ao hooks gemini `.

- [ ] **Step 2: Write failing activity tests**

Use this required mapping:

```go
var activityCases = []struct{ event, payload string; want domain.ActivityState; ok bool }{
    {"session-start", `{}`, domain.ActivityIdle, true},
    {"before-agent", `{}`, domain.ActivityActive, true},
    {"before-tool", `{}`, domain.ActivityActive, true},
    {"after-tool", `{}`, domain.ActivityActive, true},
    {"after-agent", `{}`, domain.ActivityIdle, true},
    {"notification", `{"notification_type":"ToolPermission"}`, domain.ActivityBlocked, true},
    {"notification", `{"notification_type":"other"}`, "", false},
    {"session-end", `{}`, domain.ActivityExited, true},
}
```

- [ ] **Step 3: Run tests and confirm failure**

Run: `cd backend && go test ./internal/adapters/agent/gemini ./internal/adapters/agent/activitydispatch -count=1`

- [ ] **Step 4: Implement hooks and dispatch**

Reuse `hooksjson.Manager` with millisecond timeouts. The notification hook must only invoke `ao hooks gemini notification`; it emits no Gemini hook response and therefore cannot grant or deny permission. Register `gemini.DeriveActivityState` in `activitydispatch.Derivers`; do not implement `BlockedActivitySignaler` until correlation between a permission notification and its exact completed tool is proven.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/gemini ./internal/adapters/agent/activitydispatch ./internal/cli ./internal/lifecycle -count=1`

```bash
git add backend/internal/adapters/agent/gemini/hooks.go \
  backend/internal/adapters/agent/gemini/hooks_test.go \
  backend/internal/adapters/agent/gemini/activity.go \
  backend/internal/adapters/agent/gemini/activity_test.go \
  backend/internal/adapters/agent/activitydispatch/dispatch.go \
  backend/internal/adapters/agent/activitydispatch/dispatch_test.go
git commit -m "feat: track Gemini CLI activity"
```

### Task 5: Add Gemini installation, authentication, and model readiness

**Files:**
- Create: `backend/internal/adapters/agent/gemini/auth.go`
- Create: `backend/internal/adapters/agent/gemini/auth_test.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog_test.go`
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`
- Modify: `backend/internal/service/agentauth/plans.go`
- Modify: `backend/internal/service/agentauth/plans_test.go`

**Interfaces:**
- Consumes: `ports.AgentAuthChecker`, AO installer plans, and manual model catalog policy.
- Produces: truthful Gemini readiness plus install/auth actions.

- [ ] **Step 1: Write failing readiness tests**

Require `ResolveBinary` to find `gemini`, Node-managed paths, `/usr/local/bin/gemini`, `/opt/homebrew/bin/gemini`, and Windows npm shims. Auth tests return `configured` for non-empty `GEMINI_API_KEY`, `GOOGLE_API_KEY`, or selected auth state in Gemini settings/keychain metadata; return `unknown` when only the binary exists; never report `authorized` without a provider round-trip.

- [ ] **Step 2: Write failing installer/auth-plan/model tests**

Require npm `@google/gemini-cli` on all supported platforms, Homebrew `gemini-cli` before npm on macOS, documentation URL `https://github.com/google-gemini/gemini-cli`, and a terminal setup plan launching `gemini` with guidance to choose Google login, Gemini API key, or Vertex AI. Add `gemini` to `modelcatalog.customModelEntryMode` as `ports.CustomModelEntryDirect`.

- [ ] **Step 3: Run the failing suites**

Run: `cd backend && go test ./internal/adapters/agent/gemini ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall ./internal/service/agentauth -count=1`

- [ ] **Step 4: Implement the readiness surfaces**

Reuse `binaryutil.BinarySpec`; inspect only documented environment/settings/keychain-presence signals and keep them advisory. Add `TargetGemini` to all fixed install-target validation and wire the exact npm/Homebrew plans. Add a `terminalInputPlan` only if the conformance fixture proves `/auth` is accepted after startup; otherwise use a plain terminal setup plan with no injected input.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/gemini ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall ./internal/service/agentauth -count=1`

```bash
git add backend/internal/adapters/agent/gemini backend/internal/adapters/agent/modelcatalog \
  backend/internal/service/systeminstall backend/internal/service/agentauth
git commit -m "feat: add Gemini CLI readiness and setup"
```

### Task 6: Register Gemini TUI and persist its harness identity

**Files:**
- Modify: `backend/internal/domain/harness.go`
- Modify: `backend/internal/domain/harness_test.go`
- Modify: `backend/internal/adapters/agent/registry/registry.go`
- Modify: `backend/internal/adapters/agent/registry/registry_test.go`
- Create: `backend/internal/storage/sqlite/migrations/0148_allow_gemini_harness.sql`
- Modify: `backend/internal/storage/sqlite/migrate_burned_versions_test.go`
- Modify: `backend/internal/storage/sqlite/migrate_test.go`

**Interfaces:**
- Consumes: passing Tasks 1-5.
- Produces: `domain.HarnessGemini`, selectable production adapter construction, and durable `sessions.harness = 'gemini'`.

- [ ] **Step 1: Re-run the production gate**

Run: `cd backend && AO_LIVE_GEMINI=1 go test ./internal/adapters/agent/gemini -run Live -count=1 -v`

Expected: PASS. On failure, stop before editing any file listed in this task.

- [ ] **Step 2: Write failing identity, registry, and migration tests**

Assert `HarnessGemini.IsKnown()`, one Gemini manifest in `registry.Constructors()`, a fresh migrated database accepts a Gemini session, and migration 148 is recorded in `shippedMigrations` as `0148_allow_gemini_harness.sql`.

- [ ] **Step 3: Run tests and confirm failure**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite -count=1`

- [ ] **Step 4: Add the identity, constructor, and forward-only migration**

Add `HarnessGemini AgentHarness = "gemini"` and include it in `AllHarnesses`. Register `gemini.New()` exactly once. Model migration 0148 on `0095_allow_omp_harness.sql`: update only the current `sessions` table CHECK text to append `'gemini'`, provide the inverse down migration, and append the frozen ledger entry; never edit an existing migration.

- [ ] **Step 5: Run tests and commit**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite -count=1`

```bash
git add backend/internal/domain/harness.go backend/internal/domain/harness_test.go \
  backend/internal/adapters/agent/registry \
  backend/internal/storage/sqlite/migrations/0148_allow_gemini_harness.sql \
  backend/internal/storage/sqlite/migrate_burned_versions_test.go \
  backend/internal/storage/sqlite/migrate_test.go
git commit -m "feat: register Gemini CLI harness"
```

### Task 7: Implement and prove the native Gemini ACP driver

**Files:**
- Create: `backend/internal/adapters/chatdriver/geminiacp/driver.go`
- Create: `backend/internal/adapters/chatdriver/geminiacp/driver_test.go`
- Modify: `backend/internal/adapters/chatdriver/geminiacp/live_test.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry_test.go`
- Modify: `backend/e2e/chat_persistent_acp_host_test.go`
- Modify: `backend/e2e/chat_history_test.go`

**Interfaces:**
- Consumes: `nativeacp.New`, Gemini `--acp`, the Task 2 prompt builder, and Task 3 plugin readiness.
- Produces: `geminiacp.New(plugin, promptBuilder, log) ports.ChatDriver` with streaming, tools, approvals, interrupt, and resume capabilities.

- [ ] **Step 1: Write failing driver unit tests**

Assert `configure` returns args `[]string{"--acp"}` and an env overlay containing only the AO-owned `GEMINI_SYSTEM_MD`; model is applied through ACP session option ID `model`; AO default/accept-edits/auto/bypass map to provider mode IDs `default`/`auto_edit`/`auto_edit`/`yolo`. Missing combined prompt or prompt-builder failure must prevent provider launch.

- [ ] **Step 2: Add shared ACP conformance coverage**

Use the fake ACP provider to cover initialize/auth-required, `session/new`, exact `session/load`, streamed message/thought/tool/plan updates, permission allow/deny/cancel, cancellation, process restart, daemon reconnect, and idempotent history replay. Set attachments only if the live initialize response advertises them.

- [ ] **Step 3: Run unit/e2e tests and confirm missing driver failures**

Run: `cd backend && go test ./internal/adapters/chatdriver/geminiacp ./internal/adapters/chatdriver/registry ./e2e -run 'Gemini|PersistentACP|ChatHistory' -count=1`

- [ ] **Step 4: Implement the thin binding but leave the registry unchanged**

Construct `nativeacp.Config{Harness: domain.HarnessGemini, Configure: configure, SessionMode: sessionMode, SessionOptions: sessionOptions}`. Reuse `gemini.Plugin` for binary/auth checks and the same prompt builder for TUI and Chat.

- [ ] **Step 5: Run the authenticated live matrix**

Run: `cd backend && AO_LIVE_GEMINI=1 go test ./internal/adapters/chatdriver/geminiacp -run Live -count=1 -v`

Expected: PASS for initialization/auth, streamed output, typed permission, cancellation, process restart, exact load, and duplicate-free replay. If any assertion fails, keep `geminiacp` unregistered, set no `interface.chat` capability, and ship TUI only.

- [ ] **Step 6: Register Chat only after the live matrix passes**

Add `geminiacp.New(gemini.New(), gemini.NewPromptBuilder(), log)` to `chatdriver/registry.Build`, then run:

Run: `cd backend && go test ./internal/adapters/chatdriver/... ./internal/daemon ./internal/service/chat ./internal/session_manager ./e2e -count=1`

- [ ] **Step 7: Commit**

```bash
git add backend/internal/adapters/chatdriver/geminiacp \
  backend/internal/adapters/chatdriver/registry \
  backend/e2e/chat_persistent_acp_host_test.go backend/e2e/chat_history_test.go
git commit -m "feat: add Gemini ACP chat driver"
```

### Task 8: Publish Gemini through API and product UI

**Files:**
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/controllers/agents_test.go`
- Modify: `backend/internal/httpd/apispec/openapi.yaml` (generated)
- Modify: `frontend/src/api/schema.ts` (generated)
- Modify: `packages/product-ui/src/agents.ts`
- Create: `packages/product-ui/src/agents.test.ts`
- Create: `frontend/src/renderer/assets/agents/gemini.svg`
- Modify: `frontend/src/renderer/components/AgentAvatar.tsx`
- Modify: `frontend/src/renderer/components/AgentAvatar.test.tsx`
- Modify: `packages/mobile/lib/harnessLogo.ts`
- Modify: `packages/mobile/lib/harnessLogoAssets.ts`
- Create: `packages/mobile/assets/agents/gemini.png`
- Modify: `packages/mobile/lib/harnessLogo.test.ts`

**Interfaces:**
- Consumes: registered TUI capability and optional registered Chat capability from Tasks 6-7.
- Produces: generated API enum support, selectors, and real Gemini brand assets.

- [ ] **Step 1: Write failing API and identity tests**

Add `gemini` to worker/orchestrator/session/switch DTO enums only; leave every `domain.ReviewerHarness` enum unchanged. Assert product UI identity `{id:"gemini", label:"Gemini CLI", logoKey:"gemini", initial:"G"}` and desktop/mobile logo resolution.

- [ ] **Step 2: Run tests and confirm failure**

Run: `cd backend && go test ./internal/httpd/... -count=1`

Run: `npm test -- --run packages/product-ui/src/agents.test.ts packages/mobile/lib/harnessLogo.test.ts`

- [ ] **Step 3: Add identity and real brand assets, then regenerate**

Add Gemini only to non-reviewer DTO enums and `AGENT_OPTIONS`/`AGENT_LABELS`. Use an official Gemini mark with preserved license/source metadata; do not substitute generated text artwork. Register it in the desktop `AgentAvatar` map and the mobile `LOGO_KEYS`/`LOGOS` maps. Run `npm run api` rather than editing generated schemas manually.

- [ ] **Step 4: Run API/UI tests and commit**

Run: `cd backend && go test ./internal/httpd/... -count=1`

Run: `npm run frontend:typecheck`

```bash
git add backend/internal/httpd/controllers/dto.go \
  backend/internal/httpd/controllers/agents_test.go \
  backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts \
  packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts \
  frontend/src/renderer/assets/agents/gemini.svg \
  frontend/src/renderer/components/AgentAvatar.tsx \
  frontend/src/renderer/components/AgentAvatar.test.tsx \
  packages/mobile/assets/agents/gemini.png \
  packages/mobile/lib/harnessLogo.ts packages/mobile/lib/harnessLogoAssets.ts \
  packages/mobile/lib/harnessLogo.test.ts
git commit -m "feat: expose Gemini CLI in agent selection"
```

### Task 9: Verify the complete Gemini integration

**Files:**
- Modify only if verification exposes a Gemini-scoped defect in files already listed above.

**Interfaces:**
- Consumes: all preceding Gemini tasks.
- Produces: release evidence and an independently shippable Gemini adapter.

- [ ] **Step 1: Run focused suites**

Run: `cd backend && go test ./internal/adapters/agent/gemini ./internal/adapters/agent/activitydispatch ./internal/adapters/agent/registry ./internal/adapters/chatdriver/geminiacp ./internal/adapters/chatdriver/registry ./internal/service/systeminstall ./internal/service/agentauth ./internal/session_manager ./internal/storage/sqlite ./internal/httpd/... -count=1`

- [ ] **Step 2: Run full local CI commands**

Run: `cd backend && go test ./...`

Run: `cd backend && go test -race ./...`

Run: `cd backend && go vet ./...`

Run: `npm run api`

Run: `npm run frontend:typecheck`

Run: `cd frontend && npm run build`

Run: `npx @redwoodjs/agent-ci run --all`

- [ ] **Step 3: Re-run live release gates**

Run: `cd backend && AO_LIVE_GEMINI=1 go test ./internal/adapters/agent/gemini ./internal/adapters/chatdriver/geminiacp -run Live -count=1 -v`

Record unavailable OS/account coverage as an explicit gap; do not label it passed and do not publish as validation.

- [ ] **Step 4: Audit exclusions and generated drift**

Run: `git diff --check`

Run: `git diff --name-only | rg 'reviewer|adapters/reviewer'` and require no output.

Run: `git status --short`

- [ ] **Step 5: Commit verification fixes, if any**

```bash
git add backend frontend packages
git commit -m "test: verify Gemini CLI integration"
```
