# Qoder Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Qoder CLI as an AO worker/orchestrator with deterministic TUI launch and restore, durable native activity, readiness/install support, model configuration, and native ACP Chat.

**Architecture:** A new `qoder` agent package owns executable discovery, argv construction, local auth evidence, hook installation, and hook-derived session metadata. Chat reuses AO's `nativeacp` transport and persistent host by launching the same resolved binary with `--acp`; terminal and Chat registration remain separate capability gates and do not claim TUI-to-Chat handoff.

**Tech Stack:** Go, Cobra-compatible argv construction, Qoder CLI lifecycle hooks, ACP over stdio, SQLite migrations, OpenAPI generation, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- Canonical harness ID is `qoder`; reviewer enums, registries, and execution are unchanged.
- Pin and test a minimum Qoder version before registration; all documented commands below come from `https://docs.qoder.com/cli/cli-reference.md`, `sessions.md`, `hooks-reference.md`, `authentication.md`, and `acp.md`.
- Preserve Qoder's default safety prompt by using `--append-system-prompt`, never `--system-prompt`.
- Use exact native session IDs; never restore with `--continue` or an implicit latest session.
- Merge AO-owned hooks into `.qoder/settings.json`, preserve user hooks/settings, and ignore only AO-owned sibling artifacts.
- Register Chat only after ACP authentication, load, streaming, approvals, cancellation, restart, replay, and capability tests pass.
- Do not add TUI-to-Chat handoff until a separate authenticated test proves shared identity and history.
- Support macOS, Linux, and Windows using Qoder's published platform support.

---

### Task 1: Pin and lock the upstream contract

**Files:**
- Create: `backend/internal/adapters/agent/qoder/conformance_test.go`
- Create: `backend/internal/adapters/agent/qoder/testdata/help.txt`
- Create: `backend/internal/adapters/agent/qoder/testdata/acp_initialize.json`

**Interfaces:**
- Consumes: installed `qoder` executable and Qoder's documented CLI/ACP contracts.
- Produces: a literal `minimumQoderVersion` set to the version printed by the first executable that satisfies every assertion, plus checked-in non-secret fixtures used by later adapter and Chat tests.

- [ ] **Step 1: Write the opt-in executable conformance test**

```go
func TestInstalledQoderContract(t *testing.T) {
    if os.Getenv("AO_QODER_E2E") != "1" { t.Skip("set AO_QODER_E2E=1") }
    help := runQoder(t, "--help")
    for _, flag := range []string{"--session-id", "--resume", "--prompt-interactive", "--append-system-prompt", "--model", "--reasoning-effort", "--permission-mode", "--allowed-tools", "--disallowed-tools", "--acp"} {
        if !strings.Contains(help, flag) { t.Fatalf("qoder help missing %s", flag) }
    }
}
```

- [ ] **Step 2: Capture sanitized evidence and verify the gate fails before fixtures exist**

Run: `cd backend && AO_QODER_E2E=1 go test ./internal/adapters/agent/qoder -run TestInstalledQoderContract -v`

Expected: FAIL until `help.txt` and the ACP initialize fixture are captured from the pinned executable; neither fixture may contain credentials, paths, prompts, or account identifiers.

- [ ] **Step 3: Add exact version and ACP assertions**

```go
func TestPinnedQoderACPContract(t *testing.T) {
    got := readACPInitializeFixture(t)
    requireCapability(t, got, "loadSession")
    requireCapability(t, got, "promptCapabilities")
    requireConfigOption(t, got, "model")
}
```

Record the first passing version as a literal semantic version and make older/unknown versions return `ports.ErrChatDriverIncompatible` rather than silently degrading.

- [ ] **Step 4: Run the conformance package**

Run: `cd backend && go test ./internal/adapters/agent/qoder -run 'Contract|Fixture' -v`

Expected: PASS with the checked-in sanitized fixtures; the opt-in live test skips without `AO_QODER_E2E=1`.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/qoder
git commit -m "test: pin qoder cli contract"
```

### Task 2: Implement the terminal adapter and deterministic restore

**Files:**
- Create: `backend/internal/adapters/agent/qoder/qoder.go`
- Create: `backend/internal/adapters/agent/qoder/qoder_test.go`
- Create: `backend/internal/adapters/agent/qoder/install.go`
- Create: `backend/internal/adapters/agent/qoder/auth.go`
- Create: `backend/internal/adapters/agent/qoder/auth_test.go`

**Interfaces:**
- Consumes: `ports.Agent`, `ports.AgentBinaryResolver`, `ports.AgentAuthChecker`, `binaryutil.BinarySpec`, and `minimumQoderVersion`.
- Produces: `func New() *Plugin`, `func (p *Plugin) ResolveBinary(context.Context) (string, error)`, deterministic launch/restore argv, and advisory auth status.

- [ ] **Step 1: Write table-driven command tests**

```go
tests := []struct{ name string; cfg ports.LaunchConfig; want []string }{
    {"worker", worker("task"), []string{"qoder", "--session-id", nativeID, "--permission-mode", "auto", "--prompt-interactive", "task"}},
    {"orchestrator", orchestrator("coordinate"), []string{"qoder", "--session-id", nativeID, "--permission-mode", "auto", "--prompt-interactive", "coordinate"}},
    {"configured", configured(), []string{"qoder", "--session-id", nativeID, "--model", "performance", "--reasoning-effort", "high", "--permission-mode", "accept_edits", "--append-system-prompt", "AO role", "--prompt-interactive", "task"}},
}
```

Also assert default emits no permission flag, bypass emits `bypass_permissions`, a leading-dash prompt remains one argv element, and a concrete restore case equals `[]string{"qoder", "--append-system-prompt", "AO role", "--resume", "native-123"}` with no `--continue`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/agent/qoder -run 'Launch|Restore|Auth|Binary' -v`

Expected: FAIL because `Plugin` is not implemented.

- [ ] **Step 3: Implement the adapter**

```go
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
    bin, err := p.ResolveBinary(ctx)
    if err != nil { return nil, err }
    cmd := []string{bin, "--session-id", cfg.NativeSessionID}
    cmd = appendQoderConfig(cmd, cfg.Config, cfg.Permissions, cfg.AllowedTools, cfg.DisallowedTools)
    if cfg.SystemPrompt != "" { cmd = append(cmd, "--append-system-prompt", cfg.SystemPrompt) }
    if cfg.Prompt != "" { cmd = append(cmd, "--prompt-interactive", cfg.Prompt) }
    return cmd, nil
}
```

Require non-empty `NativeSessionID` for fresh launches, return `PromptDeliveryInCommand`, expose model and effort fields, and use `binaryutil.NodeManagedUnixHomePaths("qoder")` plus `qoder`, `qoder.cmd`, and `qoder.exe` candidates.

- [ ] **Step 4: Implement conservative auth detection**

Return `configured` when `QODER_PERSONAL_ACCESS_TOKEN` is non-empty or a documented credential record exists beneath `QODER_CONFIG_DIR`/`~/.qoder`; return `unknown` for an installed CLI without positive local evidence and `unavailable` only through the readiness layer when resolution fails. Never label token presence `authorized`.

- [ ] **Step 5: Run focused tests**

Run: `cd backend && go test ./internal/adapters/agent/qoder -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/adapters/agent/qoder
git commit -m "feat: add qoder terminal adapter"
```

### Task 3: Install hooks and normalize activity

**Files:**
- Create: `backend/internal/adapters/agent/qoder/hooks.go`
- Create: `backend/internal/adapters/agent/qoder/hooks_test.go`
- Create: `backend/internal/adapters/agent/qoder/activity.go`
- Create: `backend/internal/adapters/agent/qoder/activity_test.go`
- Modify: `backend/internal/cli/hooks.go`
- Modify: `backend/internal/cli/hooks_test.go`

**Interfaces:**
- Consumes: Qoder hook stdin fields `session_id`, `transcript_path`, `hook_event_name`, and `permission_mode`; `hooksjson.Manager`; `activitydispatch`.
- Produces: workspace hook commands `ao hooks qoder <event>`, native ID capture, and AO activity facts.

- [ ] **Step 1: Write preservation and mapping tests**

```go
func TestHooksPreserveUserEntries(t *testing.T) {
    before := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-stop"}]}]}}`
    got := installHooksIntoJSON(t, before)
    require.Contains(t, got, `"command":"user-stop"`)
    require.Equal(t, 1, strings.Count(got, "ao hooks qoder stop"))
}
func TestActivityMapping(t *testing.T) {
    assertEvent(t, "session-start", domain.ActivityIdle)
    assertEvent(t, "user-prompt-submit", domain.ActivityActive)
    assertEvent(t, "permission-request", domain.ActivityBlocked)
    assertEvent(t, "stop", domain.ActivityIdle)
    assertEvent(t, "session-end", domain.ActivityExited)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/agent/qoder ./internal/cli -run Qoder -v`

Expected: FAIL with the missing qoder hook dispatcher.

- [ ] **Step 3: Implement AO-owned hook merging**

Install `SessionStart`, `UserPromptSubmit`, `PermissionRequest`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `Stop`, and `SessionEnd` command hooks in `.qoder/settings.json`. Prefix every managed command with `ao hooks qoder `, preserve unrelated JSON, deduplicate AO entries, and add only the AO hook artifact path to the sibling `.gitignore` when an artifact is required.

- [ ] **Step 4: Implement dispatch and capability claims**

```go
func (p *Plugin) EmitsSubmitActivity() bool { return true }
func (p *Plugin) EmitsBlockedActivity() bool { return true }
func (p *Plugin) FirstSignalProvesInputReady() bool { return true }
```

Only keep `EmitsBlockedActivity=true` after a test proves the Pre/Post tool correlation clears a permission-request block. Persist `session_id` from every event through the existing hook lifecycle path; observation never returns an allow decision.

- [ ] **Step 5: Run focused tests**

Run: `cd backend && go test ./internal/adapters/agent/qoder ./internal/cli -run Qoder -v`

Expected: PASS, including malformed JSON, duplicate install, uninstall, and user-hook preservation cases.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/adapters/agent/qoder backend/internal/cli/hooks.go backend/internal/cli/hooks_test.go
git commit -m "feat: track qoder session activity"
```

### Task 4: Add native ACP Chat with conformance gates

**Files:**
- Create: `backend/internal/adapters/chatdriver/qoderacp/driver.go`
- Create: `backend/internal/adapters/chatdriver/qoderacp/driver_test.go`
- Create: `backend/internal/adapters/chatdriver/qoderacp/live_test.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry_test.go`

**Interfaces:**
- Consumes: `qoder.Plugin`, `nativeacp.New`, launch command `qoder --acp`, and negotiated ACP config options.
- Produces: a `ports.ChatDriver` for `domain.HarnessQoder` with truthful capabilities.

- [ ] **Step 1: Write driver and registry tests first**

```go
func TestConfigureLaunchesNativeQoderACP(t *testing.T) {
    args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
    require.NoError(t, err)
    assert.Equal(t, []string{"--acp"}, args)
    assert.Empty(t, env)
}
```

Add shared conformance cases for initialize/auth, session/new, exact session/load, text/reasoning/tool/plan streaming, permission round-trip, cancel, process restart, daemon reconnect, replay deduplication, and advertised attachments/configuration.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/chatdriver/qoderacp ./internal/adapters/chatdriver/registry -v`

Expected: FAIL because the driver is absent.

- [ ] **Step 3: Implement the native binding**

```go
return nativeacp.New(plugin, nativeacp.Config{
    Harness: domain.HarnessQoder,
    Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
        return []string{"--acp"}, nil, nil
    },
    VersionProbe: requireMinimumQoderVersion,
}, log)
```

Map model and reasoning only through option IDs actually advertised by `getConfigOptions`; let ACP permission requests drive AO approvals. Do not implement `AgentInterfaceHandoff`.

- [ ] **Step 4: Run fake and authenticated conformance**

Run: `cd backend && go test ./internal/adapters/chatdriver/qoderacp ./internal/adapters/chatdriver/registry -v`

Run when credentials are available: `cd backend && AO_QODER_ACP_E2E=1 go test ./internal/adapters/chatdriver/qoderacp -run TestLive -v`

Expected: unit tests PASS; live test proves load/replay/cancel before registry expectation changes. If any required capability fails, leave the driver out of `registry.Build` and report Qoder as TUI-only.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/chatdriver/qoderacp backend/internal/adapters/chatdriver/registry
git commit -m "feat: add qoder acp chat driver"
```

### Task 5: Register the harness, install/auth actions, and API contract

**Files:**
- Create: `backend/internal/storage/sqlite/migrations/0148_allow_qoder_harness.sql`
- Modify: `backend/internal/domain/harness.go`
- Modify: `backend/internal/domain/projectconfig_test.go`
- Modify: `backend/internal/adapters/agent/registry/registry.go`
- Modify: `backend/internal/adapters/agent/registry/registry_test.go`
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`
- Modify: `backend/internal/service/agentauth/plans.go`
- Modify: `backend/internal/service/agentauth/plans_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/apispec/openapi.yaml`
- Modify: `frontend/src/api/schema.ts`

**Interfaces:**
- Consumes: `qoder.New()`, official install command verified from `https://docs.qoder.com/cli/installation.md`, and `qoder login`.
- Produces: persisted/selectable `qoder` harness, fixed server-owned install plan, login plan, and generated API types.

- [ ] **Step 1: Write failing enum, registry, migration, install, and auth-plan tests**

Assert `HarnessQoder == "qoder"`, `IsKnown`, one registry entry, migration acceptance/rejection, an exact approved installer argv, and login argv `[]string{"qoder", "login"}`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite/... ./internal/service/systeminstall ./internal/service/agentauth -run Qoder -v`

Expected: FAIL because Qoder is not registered.

- [ ] **Step 3: Add the production identity and plans**

```go
const HarnessQoder AgentHarness = "qoder"
```

Add only the official install recipe captured in Task 1, a docs URL of `https://docs.qoder.com/cli/installation`, and login command `qoder login`. The migration must rebuild the constrained table using the established migration pattern and add only `qoder` to the accepted values.

- [ ] **Step 4: Regenerate and verify the API**

Run: `npm run api`

Run: `cd backend && go test ./internal/httpd/...`

Expected: PASS and both generated artifacts contain `qoder` in agent/session enums without reviewer enum changes.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain backend/internal/adapters/agent/registry backend/internal/storage/sqlite/migrations/0148_allow_qoder_harness.sql backend/internal/service/systeminstall backend/internal/service/agentauth backend/internal/httpd/controllers/dto.go backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "feat: register qoder agent"
```

### Task 6: Add the frontend identity without new configuration UI

**Files:**
- Modify: `packages/product-ui/src/agents.ts`
- Create: `packages/product-ui/src/agents.test.ts`
- Create: `frontend/src/renderer/assets/agents/qoder.svg`
- Modify: `frontend/src/renderer/components/AgentAvatar.tsx`
- Modify: `frontend/src/renderer/components/AgentAvatar.test.tsx`

**Interfaces:**
- Consumes: backend readiness inventory and generated `qoder` API enum.
- Produces: accessible Qoder branding in existing agent selectors/cards; no reviewer option.

- [ ] **Step 1: Add a failing avatar mapping test**

```tsx
render(<AgentAvatar provider="qoder" />)
expect(screen.getByRole("img", { name: "Qoder" })).toBeInTheDocument()
```

Also create `packages/product-ui/src/agents.test.ts` and assert that `AGENT_OPTIONS` contains `qoder` exactly once and `AGENT_LABELS.qoder` equals `Qoder`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npm test -- AgentAvatar.test.tsx`

Expected: FAIL because the asset/mapping is absent.

- [ ] **Step 3: Add the official asset and mapping**

Add `qoder` and its label to the shared product identity catalog. Use an official Qoder brand SVG from Qoder's public brand/product assets, retain its license/source in the existing asset convention, import it in `AgentAvatar.tsx`, and map only the `qoder` agent key. Do not add bespoke selectors or reviewer UI.

- [ ] **Step 4: Run frontend checks**

Run: `npm test -- --run packages/product-ui/src/agents.test.ts frontend/src/renderer/components/AgentAvatar.test.tsx && npm run frontend:typecheck && cd frontend && npm run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts frontend/src/renderer/assets/agents/qoder.svg frontend/src/renderer/components/AgentAvatar.tsx frontend/src/renderer/components/AgentAvatar.test.tsx
git commit -m "feat: add qoder agent identity"
```

### Task 7: Run complete verification and record external gaps

**Files:**
- Modify: `docs/STATUS.md`

**Interfaces:**
- Consumes: all preceding Qoder tasks.
- Produces: release evidence that distinguishes local automated coverage from authenticated/platform coverage.

- [ ] **Step 1: Run focused and repository checks**

```bash
cd backend && go test ./internal/adapters/agent/qoder ./internal/adapters/chatdriver/qoderacp ./internal/adapters/agent/registry ./internal/adapters/chatdriver/registry
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm run frontend:typecheck
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
```

Expected: every runnable command PASS; report Docker, credential, or OS gaps by exact command and reason.

- [ ] **Step 2: Run authenticated cross-platform gates**

On each supported OS, prove fresh/restore with exact ID, worker and orchestrator prompts, permissions, hook activity, ACP load/replay/approval/cancel, and daemon reconnect. Record versions and outcomes; do not claim a platform not exercised.

- [ ] **Step 3: Update status documentation**

Document the minimum Qoder version, supported platforms, auth methods, TUI and Chat status, and the explicit exclusion of reviewer support and TUI-to-Chat handoff.

- [ ] **Step 4: Commit**

```bash
git add docs/STATUS.md
git commit -m "docs: record qoder adapter support"
```
