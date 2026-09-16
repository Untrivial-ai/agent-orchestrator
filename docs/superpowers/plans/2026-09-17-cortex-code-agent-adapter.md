# Cortex Code Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate Snowflake Cortex Code as an AO worker/orchestrator through its structured stream protocol, with registration blocked until a pinned release proves persistent sessions, append-only AO instructions, exact restore, and model control.

**Architecture:** Cortex JSON is never displayed in a terminal pane. A dedicated `cortexcode` process controller owns bidirectional stream-JSON, stdio permission mediation, session identity, resume, cancellation, and event normalization; a small agent package owns executable/auth/install discovery and only gains `ports.Agent` terminal behavior if the upstream interactive contract independently satisfies AO's no-race requirements. There is no ACP claim: Chat uses a custom `ports.ChatDriver` only after the structured protocol conformance suite passes.

**Tech Stack:** Go, Cortex Code CLI NDJSON over stdin/stdout, Snowflake connection CLI, AO Chat ports, SQLite, OpenAPI, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- Canonical harness ID is `cortex-code`; reviewer support is excluded.
- Pin the first Cortex version that proves `--input-format stream-json`, `--output-format stream-json`, `--permission-prompt-tool stdio`, explicit `--resume`, model selection, and append-only system instructions.
- Do not infer CLI flags from `cortex-code-agent-sdk`; SDK evidence only establishes capabilities to test.
- No terminal-output scraping and no JSON event stream rendered in a terminal pane.
- Store the exact `system/init.session_id`; never restore by latest session.
- Register no ACP driver because no verified native ACP endpoint exists.
- Preserve user Snowflake/Cortex configuration; AO may pass process-local flags/environment but must not rewrite `~/.snowflake/cortex`.

---

### Task 1: Create the pinned structured-protocol acceptance gate

**Files:**
- Create: `backend/internal/adapters/agent/cortexcode/conformance_test.go`
- Create: `backend/internal/adapters/agent/cortexcode/testdata/help.txt`
- Create: `backend/internal/adapters/agent/cortexcode/testdata/fresh-session.jsonl`
- Create: `backend/internal/adapters/agent/cortexcode/testdata/permission-session.jsonl`
- Create: `backend/internal/adapters/agent/cortexcode/testdata/restored-session.jsonl`

**Interfaces:**
- Consumes: official `cortex` binary and Snowflake's CLI docs at `https://docs.snowflake.com/en/user-guide/cortex-code/cortex-code-cli`.
- Produces: `minimumCortexCodeVersion` and `cortexCLIContract`, the sole source for later argv construction.

- [ ] **Step 1: Write the opt-in executable tests**

```go
func TestInstalledCortexContract(t *testing.T) {
    if os.Getenv("AO_CORTEX_CODE_E2E") != "1" { t.Skip("set AO_CORTEX_CODE_E2E=1") }
    help := runCortex(t, "--help")
    for _, flag := range []string{"--input-format", "--output-format", "--permission-prompt-tool", "--resume"} {
        if !strings.Contains(help, flag) { t.Fatalf("cortex help missing %s", flag) }
    }
}
```

The test must additionally prove the exact model flag and append-only system-instruction flag present in the installed release; a replace-only system prompt fails the gate.

- [ ] **Step 2: Prove fresh, permission, cancel, and restore behavior**

Start `cortex --input-format stream-json --output-format stream-json --permission-prompt-tool stdio`, send one JSON user message, capture `system/init.session_id`, answer one permission request without execution in the observer, cancel a running turn, terminate the process, and start a new process with `--resume <captured-id>`. Assert the restored response uses prior history.

- [ ] **Step 3: Sanitize and check in protocol fixtures**

Replace account, database, role, warehouse, path, prompt, and session values with stable fixture values while retaining event shapes and ordering. Record the exact binary version as a literal constant; make unknown or older versions fail admission.

- [ ] **Step 4: Run the acceptance gate**

Run: `cd backend && go test ./internal/adapters/agent/cortexcode -run 'Contract|Fixture' -v`

Expected: fixture tests PASS; live test skips without credentials. If append-only instructions, explicit resume, or deterministic stream input is absent, stop before production registration and document the failed semantic by exact command/output.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/cortexcode
git commit -m "test: pin cortex code protocol"
```

### Task 2: Implement executable, connection readiness, and configuration discovery

**Files:**
- Create: `backend/internal/adapters/agent/cortexcode/cortexcode.go`
- Create: `backend/internal/adapters/agent/cortexcode/cortexcode_test.go`
- Create: `backend/internal/adapters/agent/cortexcode/install.go`
- Create: `backend/internal/adapters/agent/cortexcode/auth.go`
- Create: `backend/internal/adapters/agent/cortexcode/auth_test.go`
- Create: `backend/internal/adapters/agent/cortexcode/models.go`
- Create: `backend/internal/adapters/agent/cortexcode/models_test.go`

**Interfaces:**
- Consumes: `binaryutil`, `ports.AgentBinaryResolver`, `ports.AgentAuthChecker`, `ports.AgentModelDiscoverer`, and Task 1's pinned contract.
- Produces: `func New() *Plugin`, `ResolveBinary`, advisory auth/connection status, and normalized model catalog.

- [ ] **Step 1: Write binary and auth tests**

```go
func TestAuthStatusRequiresActiveConnection(t *testing.T) {
    p := newTestPlugin(t, fakeCortex("connections", "list", activeConnectionJSON))
    assert.Equal(t, ports.AgentAuthStatusAuthorized, mustAuthStatus(t, p))
}
```

Cover binary absent, no connections, configured-but-unverified connection, valid machine-readable active connection, timeout, malformed output, and non-zero exit. Credentials must never appear in test diagnostics.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/agent/cortexcode -run 'Binary|Auth|Model' -v`

Expected: FAIL before implementation.

- [ ] **Step 3: Implement safe readiness**

Resolve `cortex`, `cortex.exe`, and `cortex.cmd` from PATH and documented install locations. Probe `cortex --version`; use the documented machine-readable form of `cortex connections list` captured in Task 1. Report `authorized` only after the command confirms an active connection, `configured` for local connection presence without validation, and `unknown` for ambiguous failures.

- [ ] **Step 4: Implement model discovery from a documented command**

```go
func (p *Plugin) Discover(ctx context.Context, req ports.AgentModelDiscoveryRequest) (ports.AgentModelCatalog, error) {
    raw, err := p.run(ctx, req.Binary, cortexCLIContract.ListModelsArgs...)
    if err != nil { return ports.AgentModelCatalog{}, err }
    return parseModelCatalog(raw)
}
```

If Task 1 finds no stable model-list command, expose `ModelSelectionText` with direct entry and validate only non-empty trimmed model IDs; do not ship a hard-coded catalog.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd backend && go test ./internal/adapters/agent/cortexcode -v`

Expected: PASS.

```bash
git add backend/internal/adapters/agent/cortexcode
git commit -m "feat: add cortex code readiness"
```

### Task 3: Implement the bidirectional stream controller

**Files:**
- Create: `backend/internal/adapters/chatdriver/cortexcode/process.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/process_unix.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/process_windows.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/protocol.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/protocol_test.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/conversation.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/conversation_test.go`

**Interfaces:**
- Consumes: `cortexCLIContract`, newline-delimited JSON stdin/stdout, `ports.ChatConversation`, `ports.ChatEvent`, and AO approval/input response types.
- Produces: `processController`, `conversation`, exact native ID capture, event normalization, permission mediation, and cancellation.

- [ ] **Step 1: Write fixture-driven normalization tests**

```go
func TestInitCapturesSessionID(t *testing.T) { assertSessionID(t, "fresh-session.jsonl", "session-fixture") }
func TestPermissionBlocksWithoutAutoApproval(t *testing.T) { assertApprovalRequested(t, "permission-session.jsonl") }
func TestResultSettlesTurn(t *testing.T) { assertTurnState(t, "fresh-session.jsonl", domain.TurnStateCompleted) }
```

Cover assistant text, reasoning, tool start/update/end, plan events, user tool results, malformed lines, stderr, provider error, EOF, result success/error, duplicate provider event IDs, and context/usage updates.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/chatdriver/cortexcode -run 'Protocol|Conversation' -v`

Expected: FAIL because the controller is absent.

- [ ] **Step 3: Implement subprocess and framing**

```go
args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio"}
if nativeID != "" { args = append(args, "--resume", nativeID) }
```

Append the verified model and system-instruction flags from Task 1. Send each user turn as one JSON line, keep stdout exclusively for protocol frames, drain bounded stderr, route permission decisions back as `control_response`, and terminate the process tree on cancellation/close using platform-specific helpers.

- [ ] **Step 4: Implement conversation semantics**

Expose provider conversation ID only after `system/init`; reject a requested resume when init returns a different ID. Map stream events to AO events without parsing presentation text. Preserve pending approval/input responders across ordinary event handling and settle every accepted prompt exactly once.

- [ ] **Step 5: Run race-focused tests and commit**

Run: `cd backend && go test -race ./internal/adapters/chatdriver/cortexcode -v`

Expected: PASS, including cancel-versus-result and close-versus-permission races.

```bash
git add backend/internal/adapters/chatdriver/cortexcode
git commit -m "feat: normalize cortex code stream protocol"
```

### Task 4: Add the custom Chat driver and persistent recovery

**Files:**
- Create: `backend/internal/adapters/chatdriver/cortexcode/driver.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/driver_test.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/history.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/history_test.go`
- Create: `backend/internal/adapters/chatdriver/cortexcode/live_test.go`
- Modify: `backend/internal/adapters/chatdriver/persistenthost/host.go`
- Modify: `backend/internal/adapters/chatdriver/persistenthost/host_test.go`

**Interfaces:**
- Consumes: Task 3's `processController`, `ports.ChatDriver`, and persistent-host ownership semantics.
- Produces: a custom driver for `domain.HarnessCortexCode` with truthful streaming/tools/approvals/interrupt/resume capabilities.

- [ ] **Step 1: Write driver conformance tests**

Test probe/version/auth, fresh start, explicit resume, history replay deduplication, approval and input responses, cancel, provider crash/restart, daemon reconnect, generation fencing, and inability to start a competing provider process.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/adapters/chatdriver/cortexcode ./internal/adapters/chatdriver/persistenthost -v`

Expected: FAIL before driver wiring.

- [ ] **Step 3: Implement the driver and host profile**

```go
func (d *Driver) Harness() domain.AgentHarness { return domain.HarnessCortexCode }
func (d *Driver) Probe(ctx context.Context) error { return d.readiness.ProbePinned(ctx) }
```

Use the exact native ID for restart, keep process-fixed model/permission/system settings immutable during live adoption, and reconcile provider replay through AO's normal projection path. Do not label the driver ACP and do not implement interface handoff.

- [ ] **Step 4: Run authenticated recovery tests**

Run: `cd backend && AO_CORTEX_CODE_E2E=1 go test ./internal/adapters/chatdriver/cortexcode -run TestLive -v`

Expected: PASS for fresh prompt, approval, cancellation, exact resume, history replay, and daemon reconnect. If any required behavior fails, do not register the driver.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/chatdriver/cortexcode backend/internal/adapters/chatdriver/persistenthost
git commit -m "feat: add cortex code chat driver"
```

### Task 5: Prove terminal eligibility or explicitly keep Chat-only

**Files:**
- Create: `backend/internal/adapters/agent/cortexcode/terminal_test.go`
- Modify: `backend/internal/adapters/agent/cortexcode/cortexcode.go`

**Interfaces:**
- Consumes: pinned interactive CLI evidence from Task 1.
- Produces: a complete `ports.Agent` only if the native TUI meets every product requirement; otherwise the package remains readiness-only and is not put in the agent registry.

- [ ] **Step 1: Test the interactive contract**

The executable test must prove a persistent human-readable TUI, deterministic initial-task delivery without blind timing, append-only instructions, exact-ID restore, model/permission reapplication, and durable activity signals. A one-shot `cortex -p` process and raw stream JSON both fail this test by definition.

- [ ] **Step 2: Implement only after the test passes**

```go
var _ ports.Agent = (*Plugin)(nil)
```

Build fresh and restore argv solely from `cortexCLIContract`; use `PromptDeliveryInCommand` only for a verified interactive prompt flag, otherwise use `PromptDeliveryAfterStart` with an authoritative readiness event. Do not add timing-only readiness patterns.

- [ ] **Step 3: Run the terminal suite**

Run: `cd backend && AO_CORTEX_CODE_E2E=1 go test ./internal/adapters/agent/cortexcode -run Terminal -v`

Expected: PASS. If it fails, remove the interface assertion and stop before Task 6: Chat infrastructure may remain tested but the production harness is not registered because the design requires worker/orchestrator support.

- [ ] **Step 4: Commit passing terminal support**

```bash
git add backend/internal/adapters/agent/cortexcode
git commit -m "feat: add cortex code terminal sessions"
```

### Task 6: Register the fully gated production harness

**Files:**
- Create: `backend/internal/storage/sqlite/migrations/0148_allow_cortex_code_harness.sql`
- Modify: `backend/internal/domain/harness.go`
- Modify: `backend/internal/domain/projectconfig_test.go`
- Modify: `backend/internal/adapters/agent/registry/registry.go`
- Modify: `backend/internal/adapters/agent/registry/registry_test.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry.go`
- Modify: `backend/internal/adapters/chatdriver/registry/registry_test.go`
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`
- Modify: `backend/internal/service/agentauth/plans.go`
- Modify: `backend/internal/service/agentauth/plans_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/apispec/openapi.yaml`
- Modify: `frontend/src/api/schema.ts`

**Interfaces:**
- Consumes: passing terminal and structured Chat gates.
- Produces: persisted/selectable `cortex-code` plus official install/auth actions and wire schema.

- [ ] **Step 1: Write failing registration and migration tests**

Assert `HarnessCortexCode == "cortex-code"`, known-harness membership, one agent and one Chat driver, migration acceptance, exact official installer plan, `cortex connections create` setup action, and no reviewer membership.

- [ ] **Step 2: Add identity and registrations**

```go
const HarnessCortexCode AgentHarness = "cortex-code"
```

Add the adapter and driver only after Tasks 4 and 5 pass. Use the official installer URL `https://ai.snowflake.com/static/cc-scripts/install.sh` through AO's downloaded-script runner; never execute a curl pipe. Add a new migration rather than editing an old one.

- [ ] **Step 3: Regenerate and verify API contracts**

Run: `npm run api`

Run: `cd backend && go test ./internal/httpd/...`

Expected: PASS; agent/session enums include `cortex-code`, reviewer enums do not.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/domain backend/internal/adapters/agent/registry backend/internal/adapters/chatdriver/registry backend/internal/storage/sqlite/migrations/0148_allow_cortex_code_harness.sql backend/internal/service/systeminstall backend/internal/service/agentauth backend/internal/httpd/controllers/dto.go backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "feat: register cortex code agent"
```

### Task 7: Add frontend identity and complete verification

**Files:**
- Modify: `packages/product-ui/src/agents.ts`
- Create: `packages/product-ui/src/agents.test.ts`
- Create: `frontend/src/renderer/assets/agents/cortex-code.svg`
- Modify: `frontend/src/renderer/components/AgentAvatar.tsx`
- Modify: `frontend/src/renderer/components/AgentAvatar.test.tsx`
- Modify: `docs/STATUS.md`

**Interfaces:**
- Consumes: registered backend readiness and generated enum.
- Produces: branded selection/card rendering and an evidence-based support statement.

- [ ] **Step 1: Add the avatar test and official asset**

```tsx
render(<AgentAvatar provider="cortex-code" />)
expect(screen.getByRole("img", { name: "Cortex Code" })).toBeInTheDocument()
```

Create `packages/product-ui/src/agents.test.ts` and assert that `AGENT_OPTIONS` contains `cortex-code` exactly once and `AGENT_LABELS["cortex-code"]` equals `Cortex Code`.

Add `cortex-code` and its label to the shared product identity catalog. Use official Snowflake/Cortex artwork and record its public source under the existing asset convention. Add no special configuration screen and no reviewer UI.

- [ ] **Step 2: Run full verification**

```bash
cd backend && go test ./internal/adapters/agent/cortexcode ./internal/adapters/chatdriver/cortexcode
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm test -- --run packages/product-ui/src/agents.test.ts frontend/src/renderer/components/AgentAvatar.test.tsx
npm run frontend:typecheck
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
```

Expected: all runnable checks PASS. Record unavailable Snowflake credentials, Docker, and untested OSes as exact gaps.

- [ ] **Step 3: Update status and commit**

Document the minimum version, supported platforms, connection readiness semantics, custom stream protocol, exact restore result, absence of ACP, and exclusions for reviewers and interface handoff.

```bash
git add packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts frontend/src/renderer/assets/agents/cortex-code.svg frontend/src/renderer/components/AgentAvatar.tsx frontend/src/renderer/components/AgentAvatar.test.tsx docs/STATUS.md
git commit -m "docs: record cortex code adapter support"
```
