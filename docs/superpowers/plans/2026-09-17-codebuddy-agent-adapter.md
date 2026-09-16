# CodeBuddy Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Tencent CodeBuddy Code as an AO worker/orchestrator and ACP Chat provider only after a pinned executable proves every required terminal and protocol capability.

**Architecture:** Start with a blocking conformance package for the official `@tencent-ai/codebuddy-code` release because its hooks are beta and its public TUI flag contract is incomplete. Once the gate passes, a `codebuddy` agent adapter owns the verified argv, local config/hook merge, identity, and readiness; a thin `nativeacp` driver launches `codebuddy --acp` and relies on AO's shared persistent ACP host.

**Tech Stack:** Go, official npm package `@tencent-ai/codebuddy-code`, CodeBuddy hooks, ACP over stdio, SQLite migrations, OpenAPI, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- Canonical harness ID is `codebuddy`; reviewer code is unchanged.
- The production gate is the exact pinned package and executable, not similarity to Claude Code or SDK-only behavior.
- Required terminal proof covers persistent interactive startup, deterministic initial task, append-only system instructions, model, all AO permission modes, exact-ID restore, hook activity, and all supported OSes.
- Required Chat proof covers ACP initialize/auth, new/load, streaming, permissions, cancel, provider restart, daemon reconnect, replay deduplication, and advertised options/attachments.
- Preserve `.codebuddy/settings.json`, `.codebuddy/settings.local.json`, and user hooks. Hook observation must not approve tools.
- Do not register TUI-to-Chat handoff without an authenticated same-ID/same-history test.

---

### Task 1: Build the blocking CodeBuddy executable contract suite

**Files:**
- Create: `backend/internal/adapters/agent/codebuddy/conformance_test.go`
- Create: `backend/internal/adapters/agent/codebuddy/testdata/package.json`
- Create: `backend/internal/adapters/agent/codebuddy/testdata/help.txt`
- Create: `backend/internal/adapters/agent/codebuddy/testdata/hook-session-start.json`
- Create: `backend/internal/adapters/agent/codebuddy/testdata/acp-initialize.json`

**Interfaces:**
- Consumes: official `@tencent-ai/codebuddy-code` npm artifact and binary aliases `codebuddy`, `cbc`, `codebuddy-code`.
- Produces: `minimumCodeBuddyVersion`, `codeBuddyCLIContract`, and sanitized fixtures that make registration mechanically conditional.

- [ ] **Step 1: Write package identity and help-contract tests**

```go
func TestPinnedPackageIdentity(t *testing.T) {
    p := readPackageFixture(t)
    assert.Equal(t, "@tencent-ai/codebuddy-code", p.Name)
    assert.Equal(t, "./bin/codebuddy", p.Bin["codebuddy"])
}

func TestTerminalContractIsComplete(t *testing.T) {
    help := readFixture(t, "help.txt")
    for _, semantic := range requiredTerminalSemantics {
        if !semantic.ProvenBy(help) { t.Fatalf("missing terminal contract: %s", semantic.Name) }
    }
}
```

`requiredTerminalSemantics` must name, without aliases, the observed flags for interactive initial prompt, append-system-prompt, model, permission, allowed/disallowed tools, exact session ID, exact restore, version, and help. A passing test must reject `--continue` as the only restore mechanism.

- [ ] **Step 2: Add an opt-in installed-binary probe**

```go
func TestInstalledCodeBuddyContract(t *testing.T) {
    if os.Getenv("AO_CODEBUDDY_E2E") != "1" { t.Skip("set AO_CODEBUDDY_E2E=1") }
    assertVersionAtLeast(t, runCodeBuddy(t, "--version"), minimumCodeBuddyVersion)
    assertHelpMatchesContract(t, runCodeBuddy(t, "--help"), codeBuddyCLIContract)
}
```

- [ ] **Step 3: Capture the pinned release fixtures**

Use `npm view @tencent-ai/codebuddy-code version dist.integrity` and inspect the downloaded package without installing it globally. Record the exact version and integrity; sanitize account IDs, paths, and tokens. Run one fresh and one restored local session to prove the flags rather than inferring them from SDK option names.

- [ ] **Step 4: Exercise ACP without a paid prompt**

Run `codebuddy --acp`, perform initialize/authenticate/session-new, and save only the sanitized initialize response. Assert load support, prompt capabilities, cancellation behavior, configuration options, and attachment claims from the response.

- [ ] **Step 5: Run the gate and stop on incomplete evidence**

Run: `cd backend && go test ./internal/adapters/agent/codebuddy -run 'Contract|Fixture' -v`

Expected: PASS. If any terminal requirement is absent, stop this plan before Tasks 2–7 and leave `codebuddy` out of domain, database, API, agent registry, Chat registry, and frontend.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/adapters/agent/codebuddy
git commit -m "test: pin codebuddy cli contract"
```

### Task 2: Implement terminal launch, restore, binary, and auth

**Files:**
- Create: `backend/internal/adapters/agent/codebuddy/codebuddy.go`
- Create: `backend/internal/adapters/agent/codebuddy/codebuddy_test.go`
- Create: `backend/internal/adapters/agent/codebuddy/install.go`
- Create: `backend/internal/adapters/agent/codebuddy/auth.go`
- Create: `backend/internal/adapters/agent/codebuddy/auth_test.go`

**Interfaces:**
- Consumes: `codeBuddyCLIContract` literals established by Task 1 and `ports.Agent`.
- Produces: `func New() *Plugin`, binary/auth capabilities, persistent terminal argv, and exact restore argv.

- [ ] **Step 1: Write table-driven launch and restore tests**

```go
func TestLaunchUsesOnlyVerifiedFlags(t *testing.T) {
    cmd := launchFor(t, ports.LaunchConfig{NativeSessionID: nativeID, Prompt: "task", SystemPrompt: "AO role"})
    assert.Equal(t, expectedCodeBuddyLaunch(codeBuddyCLIContract, nativeID, "task", "AO role"), cmd)
}
```

Cover worker and orchestrator kinds, empty prompt, leading-dash prompt, model, each AO permission mode, allow/deny tool lists, file-only system prompt input, exact restore ID, and missing ID.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/agent/codebuddy -run 'Launch|Restore|Auth|Binary' -v`

Expected: FAIL because the plugin is absent.

- [ ] **Step 3: Implement using only the captured contract**

```go
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
    bin, err := p.ResolveBinary(ctx)
    if err != nil { return nil, err }
    return codeBuddyCLIContract.Launch(bin, cfg)
}
```

The contract builder must use one argv element per value, append rather than replace the provider system prompt, require a caller-assigned native ID, and return `PromptDeliveryInCommand`. Restore must name the saved ID and reapply model, permission, tools, environment, and system instructions.

- [ ] **Step 4: Add binary and conservative auth probes**

Resolve `codebuddy`, `cbc`, and `codebuddy-code`, including npm global paths on all supported OSes. Return `configured` for non-empty `CODEBUDDY_API_KEY` or documented credential/config presence, `unknown` when installed evidence is inconclusive, and never inspect/decode tokens or call private endpoints. Preserve `CODEBUDDY_INTERNET_ENVIRONMENT` rather than synthesizing a region.

- [ ] **Step 5: Run focused tests**

Run: `cd backend && go test ./internal/adapters/agent/codebuddy -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/adapters/agent/codebuddy
git commit -m "feat: add codebuddy terminal adapter"
```

### Task 3: Merge beta hooks and derive durable activity

**Files:**
- Create: `backend/internal/adapters/agent/codebuddy/hooks.go`
- Create: `backend/internal/adapters/agent/codebuddy/hooks_test.go`
- Create: `backend/internal/adapters/agent/codebuddy/activity.go`
- Create: `backend/internal/adapters/agent/codebuddy/activity_test.go`
- Modify: `backend/internal/cli/hooks.go`
- Modify: `backend/internal/cli/hooks_test.go`

**Interfaces:**
- Consumes: `hooksjson.Manager`, CodeBuddy hook payloads, and `activitydispatch`.
- Produces: `ao hooks codebuddy <event>` dispatch, native session metadata, and activity signals.

- [ ] **Step 1: Write fixture-driven tests**

```go
func TestSessionStartCapturesNativeID(t *testing.T) { assertNativeID(t, "hook-session-start.json") }
func TestCodeBuddyActivity(t *testing.T) {
    assertState(t, "user-prompt-submit", domain.ActivityActive)
    assertState(t, "stop", domain.ActivityIdle)
    assertState(t, "session-end", domain.ActivityExited)
}
```

Add round-trip tests proving existing hooks at user, project, and project-local scopes remain byte-for-byte equivalent after install/uninstall except for AO-owned entries.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/agent/codebuddy ./internal/cli -run CodeBuddy -v`

Expected: FAIL because hook handling is absent.

- [ ] **Step 3: Install the minimum observer set**

Merge AO commands for `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, and `SessionEnd` into `<workspace>/.codebuddy/settings.json`. Add a permission event only if Task 1 captured it from the pinned runtime. Never emit hook output that grants permission.

- [ ] **Step 4: Make capability claims evidence-dependent**

Implement `EmitsSubmitActivity` and `FirstSignalProvesInputReady` after their event tests pass. Implement `EmitsBlockedActivity` only when the pinned runtime exposes a permission signal with an identifier that the Pre/Post pair demonstrably clears; otherwise map permission to `waiting_input` and omit the interface.

- [ ] **Step 5: Run focused tests**

Run: `cd backend && go test ./internal/adapters/agent/codebuddy ./internal/cli -run CodeBuddy -v`

Expected: PASS including malformed config, duplicate install, cancellation, and Windows path quoting.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/adapters/agent/codebuddy backend/internal/cli/hooks.go backend/internal/cli/hooks_test.go
git commit -m "feat: track codebuddy session activity"
```

### Task 4: Add and prove native ACP Chat

**Files:**
- Create: `backend/internal/adapters/chatdriver/codebuddyacp/driver.go`
- Create: `backend/internal/adapters/chatdriver/codebuddyacp/driver_test.go`
- Create: `backend/internal/adapters/chatdriver/codebuddyacp/extensions.go`
- Create: `backend/internal/adapters/chatdriver/codebuddyacp/live_test.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry_test.go`

**Interfaces:**
- Consumes: `codebuddy.Plugin`, `nativeacp.New`, and the official `codebuddy --acp` contract.
- Produces: Chat driver for `domain.HarnessCodeBuddy`; optional typed handling for documented `codebuddy.ai/userinfo` only.

- [ ] **Step 1: Write launch and shared conformance tests**

```go
func TestConfigure(t *testing.T) {
    args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
    require.NoError(t, err)
    assert.Equal(t, []string{"--acp"}, args)
    assert.Empty(t, env)
}
```

Exercise initialize/auth, new/load, text/reasoning/tool/plan normalization, permission responses, cancel, restart, persistent-host adoption, replay deduplication, config options, and attachment advertisement.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/chatdriver/codebuddyacp ./internal/adapters/chatdriver/registry -v`

Expected: FAIL before the driver exists.

- [ ] **Step 3: Implement the native ACP binding**

```go
return nativeacp.New(plugin, nativeacp.Config{
    Harness: domain.HarnessCodeBuddy,
    Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
        return []string{"--acp"}, nil, nil
    },
    VersionProbe: requireMinimumCodeBuddyVersion,
}, log)
```

Use advertised session options instead of assumed flag names. Keep Agent Teams extensions out of the first release unless they can be ignored without corrupting the main conversation. Do not implement interface handoff.

- [ ] **Step 4: Run authenticated live tests before registration**

Run: `cd backend && AO_CODEBUDDY_ACP_E2E=1 go test ./internal/adapters/chatdriver/codebuddyacp -run TestLive -v`

Expected: PASS for load/replay/approval/cancel/reconnect. If any required capability fails, do not add the driver to `registry.Build`; keep terminal support independent.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/chatdriver/codebuddyacp backend/internal/adapters/chatdriver/registry
git commit -m "feat: add codebuddy acp chat driver"
```

### Task 5: Register identity, install/auth plans, migration, and API

**Files:**
- Create: `backend/internal/storage/sqlite/migrations/0148_allow_codebuddy_harness.sql`
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
- Consumes: passing Tasks 1–4 and `codebuddy.New()`.
- Produces: persisted/selectable `codebuddy`, npm install method, auth setup action, and generated wire types.

- [ ] **Step 1: Write failing registration tests**

Assert the domain constant, known-harness membership, single adapter construction, database acceptance, npm package `@tencent-ai/codebuddy-code`, login/setup command captured by Task 1, and no reviewer membership.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/domain ./internal/adapters/agent/registry ./internal/storage/sqlite/... ./internal/service/systeminstall ./internal/service/agentauth -run CodeBuddy -v`

Expected: FAIL because the harness is absent.

- [ ] **Step 3: Add production registration**

```go
const HarnessCodeBuddy AgentHarness = "codebuddy"
```

Add `codebuddy.New()` to the agent registry, the npm install method, the verified native auth action, and a new migration that adds only `codebuddy`. Do not modify an existing migration.

- [ ] **Step 4: Regenerate the API and run route/spec parity**

Run: `npm run api`

Run: `cd backend && go test ./internal/httpd/...`

Expected: PASS; agent/session enums include `codebuddy`, reviewer enums do not.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain backend/internal/adapters/agent/registry backend/internal/storage/sqlite/migrations/0148_allow_codebuddy_harness.sql backend/internal/service/systeminstall backend/internal/service/agentauth backend/internal/httpd/controllers/dto.go backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "feat: register codebuddy agent"
```

### Task 6: Add frontend identity and complete verification

**Files:**
- Modify: `packages/product-ui/src/agents.ts`
- Create: `packages/product-ui/src/agents.test.ts`
- Create: `frontend/src/renderer/assets/agents/codebuddy.svg`
- Modify: `frontend/src/renderer/components/AgentAvatar.tsx`
- Modify: `frontend/src/renderer/components/AgentAvatar.test.tsx`
- Modify: `docs/STATUS.md`

**Interfaces:**
- Consumes: backend readiness inventory and generated API enum.
- Produces: branded agent presentation and recorded support boundaries.

- [ ] **Step 1: Add a failing avatar test**

```tsx
render(<AgentAvatar provider="codebuddy" />)
expect(screen.getByRole("img", { name: "CodeBuddy" })).toBeInTheDocument()
```

Create `packages/product-ui/src/agents.test.ts` and assert that `AGENT_OPTIONS` contains `codebuddy` exactly once and `AGENT_LABELS.codebuddy` equals `CodeBuddy`.

- [ ] **Step 2: Add an official, source-attributed CodeBuddy SVG and mapping**

Add `codebuddy` and its label to the shared product identity catalog. Use Tencent/CodeBuddy's public product artwork, keep the file within the existing asset size convention, and map only `codebuddy`; add no reviewer UI.

- [ ] **Step 3: Run all required verification**

```bash
cd backend && go test ./internal/adapters/agent/codebuddy ./internal/adapters/chatdriver/codebuddyacp
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm test -- --run packages/product-ui/src/agents.test.ts frontend/src/renderer/components/AgentAvatar.test.tsx
npm run frontend:typecheck
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
```

Expected: all runnable checks PASS. Record exact authenticated and Windows/macOS/Linux gaps; never substitute fake ACP coverage for vendor E2E.

- [ ] **Step 4: Update status and commit**

Document the pinned package version/integrity, beta-hook caveat, supported auth environments, terminal/Chat capability results, and excluded reviewer/handoff support.

```bash
git add packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts frontend/src/renderer/assets/agents/codebuddy.svg frontend/src/renderer/components/AgentAvatar.tsx frontend/src/renderer/components/AgentAvatar.test.tsx docs/STATUS.md
git commit -m "docs: record codebuddy adapter support"
```
