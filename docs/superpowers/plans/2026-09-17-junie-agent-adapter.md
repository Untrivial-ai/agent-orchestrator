# JetBrains Junie Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an independently shippable JetBrains Junie worker/orchestrator integration with race-free prompt delivery, lower-authority AO guidelines, durable native identity/activity/restore, truthful readiness and model controls, plus ACP Chat only after separate protocol conformance succeeds.

**Architecture:** A new `junie` adapter launches the user's installed CLI with `--prompt`, writes session-private guidelines and hook configuration under AO data, and captures Junie's generated `session_id` through `SessionStart` for exact `--resume --session-id` restores. The explicit hook config composes with Junie's default user configuration instead of modifying it, while documentation describes AO role text as guidelines rather than a privileged system prompt. Production TUI registration is blocked until hooks leave EAP and a live permission probe proves observation does not approve, deny, or suppress Junie's dialog; a thin native ACP binding remains separately unregistered until the full Chat matrix passes.

**Tech Stack:** Go adapters and tests, Junie CLI candidate build `3196.4` (marketing version `26.9.14`), Junie JSON hooks, AO's shared native ACP transport and persistent host, SQLite/goose, generated OpenAPI and TypeScript, React/React Native product surfaces, MDX documentation.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- Canonical harness ID is `junie`; one adapter serves workers and orchestrators, and reviewer support is excluded.
- Candidate build `3196.4` / marketing `26.9.14` is the conformance baseline, not a production minimum: its official hook documentation labels hooks Early Access. The production minimum becomes the first later stable build that passes every Task 1 TUI assertion.
- Do not add Junie to a production registry, SQLite harness constraint, API enum, selector, or product catalog while the pinned contract says `hooksChannel: "eap"` or any live TUI assertion fails.
- Initial tasks use `junie --prompt <task>`; terminal keystroke injection and one-shot `--task` are prohibited.
- AO standing instructions are supplied only through `--guidelines-filename <AO-owned-file>`. Product copy must say these are persistent guidelines/context, not a privileged system prompt.
- AO-owned runtime files live under `${AO_DATA_DIR}/agent-runtime/junie/<ao-session-id>/`, use owner-only permissions, and never overwrite `~/.junie`, `<project>/.junie`, project `AGENTS.md`, or user credentials.
- The AO config is added with `--config-location`; never pass `--config-default-locations false`, because Junie's default user and trusted-project configuration must continue to load and its hooks merge with AO's hooks.
- Permission observation must not change the decision: a successful synchronous `PermissionRequest` hook auto-approves, and is forbidden. The production gate must prove a safe hook form still shows the native dialog and respects an explicit denial.
- AO `default`, `acceptEdits`, and `auto` permission modes emit no Junie permission flag. Only `bypassPermissions` maps to `--brave`; do not claim Junie has exact accept-edits or automatic modes.
- Restore always uses `--resume --session-id <captured-native-id>` and reapplies model, effort, permission, config, and guidelines. Bare `--resume` is prohibited.
- Chat uses `junie --acp true` and remains absent from `chatdriver/registry` until Task 8 proves initialize/auth, new/load, streaming, permissions, cancellation, reconnect, replay, and advertised capabilities.
- TUI-to-Chat handoff is excluded even if both modes ship; do not implement `ports.AgentInterfaceHandoff` without a dedicated shared-identity/history test.
- Linux, macOS, and Windows are supported only where the official Junie installer and the gated behavior pass on that platform.
- Do not modify reviewer adapters, reviewer enums, reviewer pickers, reviewer documentation, or reviewer execution.

---

### Task 1: Pin the Junie release contract and enforce both stop gates

**Files:**
- Create: `backend/internal/adapters/agent/junie/conformance_test.go`
- Create: `backend/internal/adapters/agent/junie/testdata/contract-v3196.4.json`
- Create: `backend/internal/adapters/chatdriver/junieacp/live_test.go`

**Interfaces:**
- Consumes: installed `junie`, `AO_LIVE_JUNIE=1`, an authenticated Junie account, and a disposable test workspace.
- Produces: a reviewed candidate contract plus independent `tuiRegistrationEligible` and `chatRegistrationEligible` gates; neither gate is inferred from `--help` alone.

- [ ] **Step 1: Add the candidate release fixture and failing contract test**

Create this fixture shape with the current public release facts:

```json
{
  "build": "3196.4",
  "marketingVersion": "26.9.14",
  "releaseFeed": "https://raw.githubusercontent.com/jetbrains-junie/junie/main/update-info.jsonl",
  "hooksChannel": "eap",
  "requiredFlags": [
    "--prompt",
    "--guidelines-filename",
    "--model",
    "--effort",
    "--brave",
    "--resume",
    "--session-id",
    "--config-location",
    "--skip-update-check",
    "--acp"
  ],
  "tuiRegistrationEligible": false,
  "chatRegistrationEligible": false
}
```

The regular test decodes this file, rejects an unknown field, verifies the required flags against captured `junie --help`, and asserts that `tuiRegistrationEligible` cannot be true unless `hooksChannel == "stable"`. A future fixture update must name the first stable build and include its downloaded artifact SHA-256 for each tested platform.

- [ ] **Step 2: Add live launch, guidelines, identity, and exact-restore probes**

Under `AO_LIVE_JUNIE=1`, launch a PTY in a temporary Git repository with an AO marker in a generated guidelines file and an explicit hook config. Require `--prompt "Reply with junie-contract-ok"` to submit exactly once without terminal input; require `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `Stop`, `StopFailure`, and `SessionEnd` payloads to match their documented JSON shapes; capture a non-empty `session_id`; terminate only that process; then launch:

```text
junie --skip-update-check --config-location <ao-config> \
  --guidelines-filename <ao-guidelines> --resume --session-id <captured-id>
```

Require `SessionStart.source == "resume"`, the same `session_id`, prior conversation context, and the AO marker on a new turn. Also prove the explicit config does not suppress a harmless user-level hook or trusted-project setting and does not write either source file.

- [ ] **Step 3: Add the permission-observation safety probe**

Configure the exact `PermissionRequest` entry proposed by Task 2 against `Bash|Edit|Read` in the disposable project. Ask Junie to create a sentinel file, wait for the AO observer to record the event, and assert the native permission dialog remains visible and the sentinel remains absent. Send an explicit denial through the PTY and require the dialog to close and the sentinel to remain absent. Repeat with approval and require the action only after that approval. A synchronous hook that exits zero is never a candidate because Junie documents it as automatic approval.

- [ ] **Step 4: Add a raw ACP probe without registering Chat**

Launch `junie --acp true --skip-update-check`, initialize over stdio, create a session, stream one prompt, trigger a harmless permission request, deny it, cancel a second prompt, terminate/restart Junie, load the first provider session by its exact ID, and require duplicate-free history replay. Record the initialize capabilities and exact config option IDs for model, effort, and permission mode; assertions must fail when an expected capability is absent rather than substituting guessed IDs.

- [ ] **Step 5: Run the candidate gates and obey the stop result**

Run: `cd backend && AO_LIVE_JUNIE=1 go test ./internal/adapters/agent/junie ./internal/adapters/chatdriver/junieacp -run Live -count=1 -v`

Expected on build `3196.4`: TUI registration is blocked because official hooks are EAP, even if behavioral probes pass. If hooks are still EAP, permission observation changes the dialog/decision, generated identity cannot be captured, exact restore fails, or guidelines are not persistent, Tasks 2-5 may be developed as unregistered code but Tasks 6-8 must not add production registration or product claims. If the ACP matrix alone fails, TUI may proceed only after its own gate passes, while Chat remains unregistered.

- [ ] **Step 6: Commit the reviewed contract evidence**

```bash
git add backend/internal/adapters/agent/junie/conformance_test.go \
  backend/internal/adapters/agent/junie/testdata/contract-v3196.4.json \
  backend/internal/adapters/chatdriver/junieacp/live_test.go
git commit -m "test: pin Junie adapter contract"
```

### Task 2: Build AO-owned guidelines and hook overlays

**Files:**
- Create: `backend/internal/adapters/agent/junie/runtimefiles.go`
- Create: `backend/internal/adapters/agent/junie/runtimefiles_test.go`

**Interfaces:**
- Consumes: AO data directory, AO session ID, inline/file standing instructions, and the safe permission-hook form proven by Task 1.
- Produces: `RuntimeFileBuilder`, `RuntimeFileRequest`, `RuntimeFiles`, `NewRuntimeFileBuilder() RuntimeFileBuilder`, and atomic per-session `guidelines.md`/`config.json` files.

- [ ] **Step 1: Write failing runtime-file tests around exact types**

Define the boundary exactly:

```go
type RuntimeFileRequest struct {
    DataDir, SessionID, SystemPrompt, SystemPromptFile string
}

type RuntimeFiles struct {
    ConfigPath, GuidelinesPath string
}

type RuntimeFileBuilder interface {
    Prepare(context.Context, RuntimeFileRequest) (RuntimeFiles, error)
}
```

Tests must prove path traversal in `SessionID` is rejected; inline instructions win over `SystemPromptFile`; absent instructions still create an empty-but-valid guidelines file; files use `0600`, directories use `0700`, writes are atomic/idempotent, and concurrent calls cannot expose partial JSON or Markdown.

- [ ] **Step 2: Add the exact config-content tests**

Decode generated JSON and require only the `hooks` key with these AO commands and timeouts in seconds:

```json
{
  "hooks": {
    "SessionStart": [{"matcher":"startup|resume|clear","hooks":[{"type":"command","command":"ao hooks junie session-start","timeout":2}]}],
    "UserPromptSubmit": [{"hooks":[{"type":"command","command":"ao hooks junie user-prompt-submit","timeout":2}]}],
    "PreToolUse": [{"matcher":".*","hooks":[{"type":"command","command":"ao hooks junie pre-tool-use","timeout":2}]}],
    "Stop": [{"hooks":[{"type":"command","command":"ao hooks junie stop","timeout":2}]}],
    "StopFailure": [{"matcher":".*","hooks":[{"type":"command","command":"ao hooks junie stop-failure","timeout":2}]}],
    "SessionEnd": [{"matcher":"prompt_input_exit|logout|other","hooks":[{"type":"command","command":"ao hooks junie session-end","timeout":2}]}],
    "PermissionRequest": [{"matcher":"Bash|Edit|Read|.*","hooks":[{"type":"command","command":"ao hooks junie permission-request","timeout":2,"async":true}]}]
  }
}
```

The `PermissionRequest` entry is compiled in only for the first stable build whose Task 1 probe proves that this exact `async:true` form leaves the native dialog and user decision unchanged. The test must fail closed when the pinned contract does not carry that proof.

- [ ] **Step 3: Run tests and observe the missing implementation**

Run: `cd backend && go test ./internal/adapters/agent/junie -run RuntimeFiles -count=1`

Expected: FAIL because the builder types and constructor do not exist.

- [ ] **Step 4: Implement the isolated overlay**

Write `guidelines.md` and `config.json` beneath `agent-runtime/junie/<session-id>/`. Normalize the guidelines to `strings.TrimRight(text, "\n") + "\n"`, preserve AO text verbatim otherwise, and never read or write Junie's user/project configuration. The launch command will add this config while leaving default locations enabled, so Junie performs its documented hook merge.

- [ ] **Step 5: Run tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/junie -run RuntimeFiles -count=1`

```bash
git add backend/internal/adapters/agent/junie/runtimefiles.go \
  backend/internal/adapters/agent/junie/runtimefiles_test.go
git commit -m "feat: prepare isolated Junie runtime files"
```

### Task 3: Implement Junie TUI launch, configuration, and exact restore

**Files:**
- Create: `backend/internal/adapters/agent/junie/junie.go`
- Create: `backend/internal/adapters/agent/junie/junie_test.go`
- Create: `backend/internal/adapters/agent/junie/install.go`

**Interfaces:**
- Consumes: `ports.Agent`, `ports.AgentBinaryResolver`, and Task 2's `RuntimeFileBuilder`.
- Produces: `junie.New() *Plugin`, `ResolveJunieBinary(context.Context)`, deterministic launch/restore argv, and standard hook-derived session metadata.

- [ ] **Step 1: Write failing table-driven launch/configuration tests**

Cover worker and orchestrator launches, empty prompts, leading-dash prompts, whitespace-only model, effort `low|medium|high`, invalid effort, every AO permission mode, missing data/session values, builder failure, and Windows executable candidates. Assert these shapes:

```text
worker:       junie --skip-update-check --config-location <config> --guidelines-filename <guidelines> [--model M] [--effort E] [--brave] --prompt <task>
orchestrator: junie --skip-update-check --config-location <config> --guidelines-filename <guidelines> [--model M] [--effort E] [--brave]
restore:      junie --skip-update-check --config-location <config> --guidelines-filename <guidelines> [--model M] [--effort E] [--brave] --resume --session-id <native> [--prompt <new-turn>]
```

Assert `default`, `acceptEdits`, and `auto` add no permission argument; `bypassPermissions` adds `--brave`; no launch passes `--config-default-locations false`; and `GetPromptDeliveryStrategy` always returns `ports.PromptDeliveryInCommand`.

- [ ] **Step 2: Write failing restore and session-info tests**

Require `GetRestoreCommand` to return `ok=false` without `ports.MetadataKeyAgentSessionID`, reject an invalid/oversized native ID, name the stored ID explicitly, and reapply all configuration. `SessionInfo` must delegate to `agentbase.StandardSessionInfo`. Do not implement caller-assigned fresh identity: new Junie sessions receive the ID captured by `SessionStart`.

- [ ] **Step 3: Run the missing-adapter tests**

Run: `cd backend && go test ./internal/adapters/agent/junie -run 'Launch|Restore|Config|Binary|SessionInfo' -count=1`

- [ ] **Step 4: Implement the minimal adapter**

`GetConfigSpec` exposes `model` as `ports.ConfigFieldString` and `effort` as `ports.ConfigFieldEnum` with `[]string{"low", "medium", "high"}`. Both launch paths call `RuntimeFileBuilder.Prepare`; read `model` and `effort` from `ports.AgentConfig`; append `--prompt` as a two-element flag/value pair; and use `binaryutil.BinarySpec` for `junie`, `junie.exe`, and `junie.cmd`, including `${HOME}/.local/bin/junie`.

- [ ] **Step 5: Run adapter tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/junie -count=1`

```bash
git add backend/internal/adapters/agent/junie/junie.go \
  backend/internal/adapters/agent/junie/junie_test.go \
  backend/internal/adapters/agent/junie/install.go
git commit -m "feat: add Junie terminal adapter"
```

### Task 4: Normalize Junie activity and native identity

**Files:**
- Create: `backend/internal/adapters/agent/junie/hooks.go`
- Create: `backend/internal/adapters/agent/junie/hooks_test.go`
- Create: `backend/internal/adapters/agent/junie/activity.go`
- Create: `backend/internal/adapters/agent/junie/activity_test.go`
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch.go`
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch_test.go`
- Modify: `backend/internal/cli/hooks_test.go`

**Interfaces:**
- Consumes: Task 2's AO-owned hook config and `activitydispatch.DeriveFunc`.
- Produces: `junie.DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)`, hook preparation through `GetAgentHooks`, and native ID persistence from `session_id`.

- [ ] **Step 1: Write failing lifecycle mapping tests**

Use this table exactly:

```go
var activityCases = []struct {
    event, payload string
    want domain.ActivityState
    ok bool
}{
    {"session-start", `{"session_id":"junie-1","source":"startup"}`, "", false},
    {"user-prompt-submit", `{"session_id":"junie-1","prompt":"work"}`, domain.ActivityActive, true},
    {"pre-tool-use", `{"tool_name":"Bash"}`, domain.ActivityActive, true},
    {"permission-request", `{"tool_name":"Bash"}`, domain.ActivityBlocked, true},
    {"stop", `{"last_assistant_message":"done"}`, domain.ActivityIdle, true},
    {"stop-failure", `{"error":"rate_limit"}`, domain.ActivityWaitingInput, true},
    {"session-end", `{"reason":"prompt_input_exit"}`, domain.ActivityExited, true},
    {"unknown", `{}`, "", false},
}
```

Also assert malformed JSON never invents a native ID and that `session-start` is metadata-only while `ao hooks` still sends `agentSessionId: "junie-1"` to the daemon.

- [ ] **Step 2: Write failing hook-installation tests**

`GetAgentHooks` must call the same runtime builder used by launch with `WorkspaceHookConfig.DataDir`, `SessionID`, and standing instructions. Assert it writes no file under the worktree or `~/.junie`, retains default config loading, and generates the exact Task 2 command set once. `PermissionRequest` must be absent unless the stable pinned contract includes the passing safety probe.

- [ ] **Step 3: Run the failing suites**

Run: `cd backend && go test ./internal/adapters/agent/junie ./internal/adapters/agent/activitydispatch ./internal/cli -run 'Junie|RuntimeFiles' -count=1`

- [ ] **Step 4: Implement dispatch and hook preparation**

Register `domain.AgentHarness("junie")` in `activitydispatch.Derivers` so the generic hook receiver both derives activity and extracts `session_id`. Keep SessionStart free of activity promotion, because startup alone is not an active model turn. Do not add terminal scraping or a Junie-specific Kanban mapper.

- [ ] **Step 5: Run focused lifecycle tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/junie ./internal/adapters/agent/activitydispatch ./internal/cli ./internal/lifecycle -count=1`

```bash
git add backend/internal/adapters/agent/junie \
  backend/internal/adapters/agent/activitydispatch/dispatch.go \
  backend/internal/adapters/agent/activitydispatch/dispatch_test.go \
  backend/internal/cli/hooks_test.go
git commit -m "feat: track Junie session activity"
```

### Task 5: Add Junie installation, authentication, and model readiness

**Files:**
- Create: `backend/internal/adapters/agent/junie/auth.go`
- Create: `backend/internal/adapters/agent/junie/auth_test.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog_test.go`
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`
- Modify: `backend/internal/service/agentauth/plans.go`
- Modify: `backend/internal/service/agentauth/plans_test.go`

**Interfaces:**
- Consumes: `ports.AgentAuthChecker`, official Junie installers, and AO's manual model-entry policy.
- Produces: truthful installed/auth snapshots, setup actions, and free-form model configuration.

- [ ] **Step 1: Write failing binary and auth tests**

Require resolution from `PATH`, `${HOME}/.local/bin/junie`, and supported Windows shim locations. `AuthStatus` returns `configured` for a non-empty `JUNIE_API_KEY`, `JUNIE_ANTHROPIC_API_KEY`, `JUNIE_OPENAI_API_KEY`, `JUNIE_GOOGLE_API_KEY`, `JUNIE_GROK_API_KEY`, `JUNIE_META_API_KEY`, `JUNIE_OPENROUTER_API_KEY`, or `JUNIE_LITELLM_API_KEY`; returns `unknown` when only the binary or opaque OS credential-store state exists; and never returns `authorized` without a provider round-trip.

- [ ] **Step 2: Write failing installer/setup/model tests**

Add `TargetJunie` to fixed target validation. Require the official Unix installer `https://junie.jetbrains.com/install.sh` with `bash`, the official Windows installer `https://junie.jetbrains.com/install.ps1`, and docs URL `https://junie.jetbrains.com/docs/junie-cli.html`. Add a terminal setup plan that launches `junie` without injected keystrokes and tells the user to choose JetBrains account, Junie API key, or BYOK in the welcome screen. Add `junie` to `modelcatalog.customModelEntryMode` as `ports.CustomModelEntryDirect`.

- [ ] **Step 3: Run the failing readiness suites**

Run: `cd backend && go test ./internal/adapters/agent/junie ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall ./internal/service/agentauth -count=1`

- [ ] **Step 4: Implement only documented readiness signals**

Use `binaryutil.BinarySpec` and environment-name presence checks without reading secret values into logs. Do not parse secure credential stores or launch a model request from catalog refresh. The authenticated CLI handshake remains authoritative at session launch.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/junie ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall ./internal/service/agentauth -count=1`

```bash
git add backend/internal/adapters/agent/junie \
  backend/internal/adapters/agent/modelcatalog \
  backend/internal/service/systeminstall backend/internal/service/agentauth
git commit -m "feat: add Junie readiness and setup"
```

### Task 6: Register Junie TUI and persist its harness identity

**Files:**
- Modify: `backend/internal/domain/harness.go`
- Modify: `backend/internal/domain/harness_test.go`
- Modify: `backend/internal/adapters/agent/registry/registry.go`
- Modify: `backend/internal/adapters/agent/registry/registry_test.go`
- Create: `backend/internal/storage/sqlite/migrations/0148_allow_junie_harness.sql`
- Modify: `backend/internal/storage/sqlite/migrate_burned_versions_test.go`
- Modify: `backend/internal/storage/sqlite/migrate_test.go`

**Interfaces:**
- Consumes: passing stable TUI gate from Task 1 and Tasks 2-5.
- Produces: `domain.HarnessJunie`, one production constructor, and durable `sessions.harness = 'junie'`.

- [ ] **Step 1: Re-run and enforce the production TUI gate**

Run: `cd backend && AO_LIVE_JUNIE=1 go test ./internal/adapters/agent/junie -run Live -count=1 -v`

Expected: PASS with a reviewed fixture whose `hooksChannel` is `stable`, whose build is no older than `3196.4`, and whose permission test proves observation leaves the native dialog and decision intact. On any failure, stop before editing a file in this task.

- [ ] **Step 2: Write failing identity, registry, and migration tests**

Assert `HarnessJunie.IsKnown()`, exactly one Junie manifest in `registry.Constructors()`, a fresh migrated database accepts a Junie session, and migration 148 is recorded as `0148_allow_junie_harness.sql` in `shippedMigrations`.

- [ ] **Step 3: Run tests and confirm the registration is absent**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite -count=1`

- [ ] **Step 4: Add the identity, constructor, and forward-only migration**

Add `HarnessJunie AgentHarness = "junie"` to `AllHarnesses` and register `junie.New()` exactly once. Base migration 0148 on the current harness-constraint migration, append only `'junie'`, add its inverse down migration, and freeze the ledger entry; never edit an already-shipped migration. If another independently planned adapter has landed migration 0148 first, allocate the next unused migration number and update both migration tests in the same commit.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite -count=1`

```bash
git add backend/internal/domain/harness.go backend/internal/domain/harness_test.go \
  backend/internal/adapters/agent/registry \
  backend/internal/storage/sqlite/migrations/0148_allow_junie_harness.sql \
  backend/internal/storage/sqlite/migrate_burned_versions_test.go \
  backend/internal/storage/sqlite/migrate_test.go
git commit -m "feat: register Junie terminal harness"
```

### Task 7: Publish truthful Junie API, UI, assets, and documentation

**Files:**
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/controllers/agents_test.go`
- Modify: `backend/internal/httpd/apispec/openapi.yaml` (generated)
- Modify: `frontend/src/api/schema.ts` (generated)
- Modify: `packages/product-ui/src/agents.ts`
- Create: `packages/product-ui/src/agents.test.ts`
- Create: `frontend/src/renderer/assets/agents/junie.svg`
- Modify: `frontend/src/renderer/components/AgentAvatar.tsx`
- Modify: `frontend/src/renderer/components/AgentAvatar.test.tsx`
- Create: `packages/mobile/assets/agents/junie.png`
- Modify: `packages/mobile/lib/harnessLogo.ts`
- Modify: `packages/mobile/lib/harnessLogoAssets.ts`
- Modify: `packages/mobile/lib/harnessLogo.test.ts`
- Create: `frontend/src/landing/public/docs/logos/junie.svg`
- Create: `frontend/src/landing/content/docs/plugins/agents/junie.mdx`
- Modify: `frontend/src/landing/content/docs/plugins/agents/meta.json`
- Modify: `frontend/src/landing/content/docs/plugins/agents/index.mdx`

**Interfaces:**
- Consumes: registered TUI capability from Task 6; Chat remains false unless Task 8 also passes and registers.
- Produces: generated API enum support, desktop/mobile identity and official assets, and documentation that states the real instruction/permission semantics.

- [ ] **Step 1: Write failing API and identity tests**

Add `junie` to worker/orchestrator/session/switch DTO enums only and assert every reviewer enum is unchanged. Require product identity `{id:"junie", label:"Junie", logoKey:"junie", initial:"J"}` and successful desktop/mobile asset resolution.

- [ ] **Step 2: Write the documentation assertions before the page**

Extend the landing Markdown-twin/content checks to require the Junie page and these claims:

```text
AO supplies role instructions through Junie guidelines. Guidelines are persistent context, not a privileged system prompt.
Default, Accept edits, and Auto preserve Junie's native approval behavior; Bypass permissions enables Junie Brave Mode.
AO resumes only the exact native Junie session ID captured at startup.
```

The page must not claim Chat unless Task 8 has passed and its registry change is in the same branch.

- [ ] **Step 3: Run tests and confirm failure**

Run: `cd backend && go test ./internal/httpd/... -count=1`

Run: `npm test -- --run packages/product-ui/src/agents.test.ts frontend/src/renderer/components/AgentAvatar.test.tsx packages/mobile/lib/harnessLogo.test.ts`

Run: `cd frontend/src/landing && npm test -- --run scripts/generate-markdown-twins.test.mjs`

- [ ] **Step 4: Add the product surface and regenerate contracts**

Use an official JetBrains Junie mark with source/license metadata, rasterize the mobile copy at the repository's expected size, add the non-reviewer DTO values and product identities, and run `npm run api` instead of manually editing generated artifacts. Add the MDX page to `meta.json` and the agent index; describe the current minimum stable passing build from Task 1, exact restore, model/effort, Brave Mode mapping, and the lower-authority guidelines boundary.

- [ ] **Step 5: Run API/UI/docs tests and commit**

Run: `cd backend && go test ./internal/httpd/... -count=1`

Run: `npm run frontend:typecheck`

Run: `cd frontend/src/landing && npm test -- --run scripts/generate-markdown-twins.test.mjs`

```bash
git add backend/internal/httpd backend/internal/httpd/apispec/openapi.yaml \
  frontend/src/api/schema.ts frontend/src/renderer/assets/agents/junie.svg \
  frontend/src/renderer/components/AgentAvatar.tsx \
  frontend/src/renderer/components/AgentAvatar.test.tsx \
  frontend/src/landing/content/docs/plugins/agents \
  frontend/src/landing/public/docs/logos/junie.svg packages/product-ui/src/agents.ts \
  packages/product-ui/src/agents.test.ts packages/mobile/assets/agents/junie.png \
  packages/mobile/lib/harnessLogo.ts packages/mobile/lib/harnessLogoAssets.ts \
  packages/mobile/lib/harnessLogo.test.ts
git commit -m "feat: expose Junie in agent selection"
```

### Task 8: Implement the native ACP binding, then register only on conformance

**Files:**
- Create: `backend/internal/adapters/chatdriver/junieacp/driver.go`
- Create: `backend/internal/adapters/chatdriver/junieacp/driver_test.go`
- Modify: `backend/internal/adapters/chatdriver/junieacp/live_test.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry_test.go`
- Modify: `backend/e2e/chat_persistent_acp_host_test.go`
- Modify: `backend/e2e/chat_history_test.go`
- Modify: `frontend/src/landing/content/docs/plugins/agents/junie.mdx`

**Interfaces:**
- Consumes: `nativeacp.New`, `junie --acp true`, Task 2's `RuntimeFileBuilder`, and Task 3's plugin readiness.
- Produces: `junieacp.New(plugin, runtimeFiles, log) ports.ChatDriver` only after live proof of every advertised capability.

- [ ] **Step 1: Write failing thin-binding tests**

`configure` must prepare the AO-owned guidelines/config using `acpdriver.LaunchConfig{SessionID, DataDir, WorkspacePath, SystemPrompt}` and return:

```text
--acp true --skip-update-check --config-location <config> --guidelines-filename <guidelines>
```

Require no hook-derived behavior in ACP because Junie's docs say ACP/server hosts invoke no hooks. Test model and effort only through provider-advertised ACP option IDs captured by Task 1, map permission only through advertised modes, reject unsupported requested settings, and set attachment capabilities only when initialize advertises them.

- [ ] **Step 2: Add shared ACP unit/e2e coverage**

Use the fake ACP provider to prove initialize/auth-required, `session/new`, exact `session/load`, assistant/reasoning/tool/plan streaming, permission allow/deny/cancel, prompt cancellation, process restart, daemon reconnect, and idempotent history replay. Assert AO's provider conversation ID is the ID returned by ACP, not Junie's TUI `session_id`.

- [ ] **Step 3: Run tests and confirm the binding is not registered**

Run: `cd backend && go test ./internal/adapters/chatdriver/junieacp ./internal/adapters/chatdriver/registry ./e2e -run 'Junie|PersistentACP|ChatHistory' -count=1`

- [ ] **Step 4: Implement the driver without changing the registry**

Construct `nativeacp.Config{Harness: domain.HarnessJunie, Configure: configure, SessionOptions: sessionOptions, ValidateTurnSettings: validateTurnSettings}` and use `junie.Plugin` for binary/auth probes. Keep `AgentInterfaceHandoff` absent because TUI and ACP identity equivalence is outside this plan.

- [ ] **Step 5: Run the authenticated live Chat matrix**

Run: `cd backend && AO_LIVE_JUNIE=1 go test ./internal/adapters/chatdriver/junieacp -run Live -count=1 -v`

Expected: PASS for initialization/auth, new/load, streamed content and tools, typed permission round-trips, denial, cancellation, process restart, daemon reconnect, and duplicate-free replay. If any assertion fails, leave `junieacp` unregistered, do not advertise Chat in API/UI/docs, and commit only the unregistered driver and conformance evidence if it remains useful.

- [ ] **Step 6: Register and document Chat only after the matrix passes**

Add `junieacp.New(junie.New(), junie.NewRuntimeFileBuilder(), log)` to `chatdriver/registry.Build`, assert `SupportsChat(domain.HarnessJunie)`, and update the Junie MDX page from TUI-only to native ACP Chat. Then run:

Run: `cd backend && go test ./internal/adapters/chatdriver/... ./internal/daemon ./internal/service/chat ./internal/session_manager ./e2e -count=1`

- [ ] **Step 7: Commit the truthful result**

```bash
git add backend/internal/adapters/chatdriver/junieacp \
  backend/internal/adapters/chatdriver/registry \
  backend/e2e/chat_persistent_acp_host_test.go backend/e2e/chat_history_test.go \
  frontend/src/landing/content/docs/plugins/agents/junie.mdx
git commit -m "feat: add Junie ACP chat driver"
```

### Task 9: Verify the complete Junie integration and exclusions

**Files:**
- Modify only when verification exposes a Junie-scoped defect in a file already listed above.

**Interfaces:**
- Consumes: all completed Junie tasks and recorded gate outcomes.
- Produces: release evidence for the exact shipped TUI/Chat capability set.

- [ ] **Step 1: Run focused suites**

Run: `cd backend && go test ./internal/adapters/agent/junie ./internal/adapters/agent/activitydispatch ./internal/adapters/agent/registry ./internal/adapters/chatdriver/junieacp ./internal/adapters/chatdriver/registry ./internal/service/systeminstall ./internal/service/agentauth ./internal/session_manager ./internal/storage/sqlite ./internal/httpd/... -count=1`

- [ ] **Step 2: Run the complete local CI command set**

Run: `cd backend && go test ./...`

Run: `cd backend && go test -race ./...`

Run: `cd backend && go vet ./...`

Run: `npm run api`

Run: `npm run frontend:typecheck`

Run: `cd frontend && npm run build`

Run: `npx @redwoodjs/agent-ci run --all`

- [ ] **Step 3: Re-run the live gates on supported platforms**

Run: `cd backend && AO_LIVE_JUNIE=1 go test ./internal/adapters/agent/junie ./internal/adapters/chatdriver/junieacp -run Live -count=1 -v`

Record macOS, Linux, Windows, account, and ACP cases that could not run as explicit gaps. A missing platform/account is not a pass, and publishing is never a validation step.

- [ ] **Step 4: Audit generated drift, capability claims, and reviewer exclusion**

Run: `git diff --check`

Run: `git diff --name-only | rg 'reviewer|adapters/reviewer'` and require no output.

Run: `rg -n 'Junie.*system prompt|system prompt.*Junie' frontend/src/landing/content/docs/plugins/agents/junie.mdx` and require no claim that guidelines have system-prompt authority.

Run: `git status --short`

- [ ] **Step 5: Commit verification fixes, if any**

```bash
git add backend frontend packages
git commit -m "test: verify Junie integration"
```
