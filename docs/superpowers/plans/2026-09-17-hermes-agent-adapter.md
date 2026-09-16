# Hermes Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Add a production Hermes Agent integration that can serve AO workers and orchestrators, restore exact native sessions, report durable activity, and use Hermes ACP for Chat without claiming unverified capabilities.

**Architecture:** Build one Hermes agent package for binary/auth/model/TUI behavior and one thin Hermes ACP binding over AO's existing native ACP transport. Upstream behavior is executable evidence: production registration happens only after a pinned Hermes release proves a per-process standing-instruction file, an isolated observer-plugin path, exact TUI session identity, and the complete ACP contract.

**Tech Stack:** Go 1.25, Hermes Agent 0.21.3 or newer, Python 3.11-3.13, Agent Client Protocol, SQLite migrations, OpenAPI, React/TypeScript, Vitest.

**Spec:** docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md

## Global Constraints

- The canonical harness ID is hermes.
- The same harness serves worker and orchestrator roles; AO constructs both standing prompts in backend/internal/session_manager/prompt.go.
- Never write or replace user SOUL.md, config.yaml, .env, plugins, or project context files.
- Never place AO standing instructions in a user message.
- Never infer installation, auth, restore, activity, or ACP support from help text alone.
- Restore always names the captured Hermes session ID; --continue, latest, title matching, and terminal scraping are prohibited.
- The observer is read-only: it emits AO lifecycle facts and never answers an approval.
- TUI-to-Chat handoff is excluded. Do not implement ports.AgentInterfaceHandoff.
- Reviewer adapters, reviewer enums, reviewer assets, and reviewer UI are excluded.
- Linux, macOS, and Windows commands must follow Hermes' supported platforms.
- If the standing-prompt conformance gate fails, stop before adding hermes to any production registry, DTO enum, migration, or UI picker.
- If the observer/native-ID gate fails but ACP passes, do not register TUI support.
- If any ACP gate fails, do not add Hermes to the Chat registry.

---

### Task 1: Establish the pinned upstream contract and stop gates

**Files:**
- Create: backend/internal/adapters/agent/hermes/contract.go
- Create: backend/internal/adapters/agent/hermes/contract_test.go
- Create: backend/internal/adapters/agent/hermes/upstream_conformance_test.go
- Create: backend/internal/adapters/agent/hermes/testdata/observer-event.json
- Create: docs/harnesses/hermes.md

**Interfaces:**
- Produces: hermes.Contract with Version, SystemPromptFile, ObserverPlugin, TUISessionID, ACP, and AuthProbe evidence.
- Produces: ValidateContract(Contract) error.
- Produces: MinimumVersion string containing the exact first upstream version that passes every enabled production gate.
- Consumes: AO_HERMES_CONFORMANCE_BINARY for opt-in tests against a real installation.

- [ ] **Step 1: Write failing contract tests**

~~~go
func TestValidateContractRequiresStandingInstructions(t *testing.T) {
	err := ValidateContract(Contract{Version: "0.21.3", ACP: true})
	if !errors.Is(err, ErrSystemPromptContractMissing) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateContractRequiresObserverForTUI(t *testing.T) {
	err := ValidateContract(Contract{
		Version: "0.21.3", SystemPromptFile: true, ACP: true, TUI: true,
	})
	if !errors.Is(err, ErrObserverContractMissing) {
		t.Fatalf("error = %v", err)
	}
}
~~~

The live conformance test must start Hermes with a temporary HOME, HERMES_HOME, workspace, and loopback fake OpenAI-compatible provider. It must assert:

1. The candidate release is at least 0.21.3.
2. A per-process system-prompt-file input reaches both fresh and resumed TUI/ACP requests, preserves Hermes' built-in safety prompt, and never writes the prompt to user chat history.
3. A per-process observer-plugin input loads only the supplied AO-owned file and emits session start, prompt start, permission pending/resolved, turn settled, and session exit with the exact native session ID.
4. hermes chat -q starts an interactive session on a PTY without a second terminal write.
5. hermes chat --resume SESSION_ID resumes that exact ID.
6. hermes acp --check succeeds and ACP initialize, session/new, prompt, cancel, process restart, session/load, and history replay work against one session ID.
7. A failed credential is distinguishable from a missing executable without logging the credential.

- [ ] **Step 2: Run unit and live tests and record the stop decision**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes -run TestValidateContract -count=1
AO_HERMES_CONFORMANCE_BINARY=/absolute/path/to/hermes \
  go test ./internal/adapters/agent/hermes -run TestHermesUpstreamConformance -count=1 -v
~~~

Expected before an upstream standing-prompt/plugin release: the live test fails with ErrSystemPromptContractMissing or ErrObserverContractMissing. Stop the implementation at this point and publish only the evidence note; do not execute Tasks 2-7.

- [ ] **Step 3: Implement the immutable contract validator**

~~~go
type Contract struct {
	Version          string
	SystemPromptFile bool
	ObserverPlugin   bool
	TUISessionID     bool
	TUI              bool
	ACP              bool
	ACPLoad          bool
	ACPReplay        bool
	ACPPermissions   bool
	ACPCancel        bool
	AuthProbe        bool
}

func ValidateContract(c Contract) error
~~~

Use a package-local three-component semantic-version parser following backend/internal/adapters/chatdriver/kimchiacp/version.go, so this gate adds no dependency. The live test writes no repository file; once it passes, set MinimumVersion in contract.go to the exact printed version and document the tested commands, hashes, operating systems, provider stub, and results in docs/harnesses/hermes.md.

- [ ] **Step 4: Re-run the contract package**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes -count=1
~~~

Expected: PASS, including a unit assertion that the recorded MinimumVersion is not lower than 0.21.3.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/hermes docs/harnesses/hermes.md
git commit -m "test: pin Hermes adapter contract"
~~~

### Task 2: Implement the Hermes TUI adapter and safe prompt delivery

**Files:**
- Create: backend/internal/adapters/agent/hermes/hermes.go
- Create: backend/internal/adapters/agent/hermes/hermes_test.go
- Create: backend/internal/adapters/agent/hermes/install.go
- Create: backend/internal/adapters/agent/hermes/install_test.go

**Interfaces:**
- Consumes: ports.Agent, ports.AgentBinaryResolver, ports.AgentExitDetector, ports.LaunchConfig, ports.RestoreConfig.
- Produces: hermes.New() *Plugin.
- Produces: ResolveHermesBinary(context.Context) (string, error).
- Produces: fresh argv hermes chat --query-file TASK_FILE --system-prompt-file SYSTEM_FILE plus model/provider/permission flags proven in Task 1.
- Produces: restore argv with --resume exact-session-id and the same standing-instruction/model/permission inputs.

- [ ] **Step 1: Write table-driven failing command tests**

~~~go
func TestLaunchAndRestoreCommands(t *testing.T) {
	tests := []struct {
		name string
		cfg  ports.LaunchConfig
		want []string
	}{
		{"worker task and system file", launchConfig("fix tests"), []string{
			"/resolved/hermes", "chat", "--query", "fix tests",
			"--system-prompt-file", "/ao/system.md",
		}},
		{"model and bypass", launchConfigWithModel("nous:Hermes-4", ports.PermissionModeBypassPermissions), []string{
			"/resolved/hermes", "chat", "--query", "fix tests",
			"--system-prompt-file", "/ao/system.md", "--model", "nous:Hermes-4", "--yolo",
		}},
	}
	// Assert exact argv and no shell concatenation.
}
~~~

Also assert blank prompts, missing AO prompt files, accept-edits/auto mappings not proved by Task 1, absent native IDs, cancellation, and Windows .exe/.cmd resolution.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes -run 'Test(Launch|Restore|Resolve)' -count=1
~~~

Expected: compilation failure because Plugin does not exist.

- [ ] **Step 3: Implement the minimal adapter**

~~~go
func (p *Plugin) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error)
func (p *Plugin) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error)
func (p *Plugin) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error)
func (p *Plugin) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error)
func (p *Plugin) ResolveBinary(context.Context) (string, error)
func (p *Plugin) ExitDetectionMode() ports.AgentExitDetectionMode
~~~

Use agentbase.StandardSessionInfo and binaryutil with hermes/hermes.exe/hermes.cmd plus the documented ~/.local/bin and Windows install locations. Pass the initial task as a distinct --query argv value; Hermes documents that it is submitted literally on a real TTY, so quotes, substitutions, and leading hyphens never enter a shell.

- [ ] **Step 4: Run adapter tests**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/hermes
git commit -m "feat: add Hermes terminal adapter"
~~~

### Task 3: Install the isolated observer and normalize activity

**Files:**
- Create: backend/internal/adapters/agent/hermes/hooks.go
- Create: backend/internal/adapters/agent/hermes/hooks_test.go
- Create: backend/internal/adapters/agent/hermes/activity.go
- Create: backend/internal/adapters/agent/hermes/activity_test.go
- Create: backend/internal/adapters/agent/hermes/assets/ao_observer.py
- Modify: backend/internal/adapters/agent/activitydispatch/dispatch.go
- Modify: backend/internal/adapters/agent/activitydispatch/dispatch_test.go

**Interfaces:**
- Consumes: the exact per-process observer-plugin argument proven in Task 1 and AO_SESSION_ID.
- Produces: an AO-owned observer at DATA_DIR/agent-runtime/hermes/ao_observer.py.
- Produces: DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool).
- Produces: native-session metadata through the existing ao hooks session-start payload path.

- [ ] **Step 1: Write failing hook and activity tests**

~~~go
func TestDeriveActivityState(t *testing.T) {
	tests := []struct{ event string; want domain.ActivityState }{
		{"session-start", domain.ActivityIdle},
		{"prompt-start", domain.ActivityActive},
		{"tool-start", domain.ActivityActive},
		{"permission-pending", domain.ActivityBlocked},
		{"permission-resolved", domain.ActivityActive},
		{"turn-settled", domain.ActivityIdle},
		{"session-exit", domain.ActivityExited},
	}
	// Assert every mapping and reject unknown events.
}

func TestGetAgentHooksWritesOnlyAODataDir(t *testing.T) {
	// Assert atomic 0600 write below DataDir, byte preservation on repeat,
	// and zero writes under WorkspacePath or a synthetic HERMES_HOME.
}
~~~

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes ./internal/adapters/agent/activitydispatch -run 'Test(Hermes|DeriveActivity|GetAgentHooks)' -count=1
~~~

- [ ] **Step 3: Implement the observer**

The Python asset registers only on_session_start, pre_llm_call, pre_tool_call, pre_approval_request, post_approval_response, post_llm_call, on_session_end, and on_session_finalize. It invokes AO as an argv array, sends bounded JSON on stdin, forwards session_id as agentSessionId, accepts no response, and returns no hook decision.

~~~go
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)
~~~

Add hermes routing to activitydispatch. Reject missing/mismatched AO_SESSION_ID, oversized payloads, and events without an exact native session ID at session start.

- [ ] **Step 4: Run hook tests**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes ./internal/adapters/agent/activitydispatch -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/hermes backend/internal/adapters/agent/activitydispatch
git commit -m "feat: observe Hermes session activity"
~~~

### Task 4: Add readiness, authentication, and model configuration

**Files:**
- Create: backend/internal/adapters/agent/hermes/auth.go
- Create: backend/internal/adapters/agent/hermes/auth_test.go
- Modify: backend/internal/adapters/agent/hermes/hermes.go
- Modify: backend/internal/adapters/agent/modelcatalog/catalog.go
- Modify: backend/internal/adapters/agent/modelcatalog/catalog_test.go
- Modify: backend/internal/service/systeminstall/agentplans.go
- Modify: backend/internal/service/systeminstall/agentplans_test.go

**Interfaces:**
- Consumes: hermes auth status and hermes model/catalog output proven by Task 1.
- Produces: AuthStatus(context.Context) (ports.AgentAuthStatus, error).
- Produces: a text model control if no stable machine-readable catalog exists; otherwise a normalized provider:model catalog.
- Produces: an install help plan pointing to the official Hermes installer, never an automatic curl-pipe execution.

- [ ] **Step 1: Write failing auth/model tests**

Test missing binary => unavailable, explicit missing credential => unauthorized, local key/OAuth presence => configured, proved provider round-trip => authorized, corrupt/changed output => unknown, timeout/cancellation, secret redaction, provider:model forwarding, and catalog normalization.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall -run 'Test(Hermes|hermes)' -count=1
~~~

- [ ] **Step 3: Implement bounded probes**

Do not equate credential presence with validity. Parse only the structured form recorded by the conformance fixture; if Hermes exposes only human text for the pinned version, return configured or unknown and let ACP/TUI launch correct readiness after a provider response.

- [ ] **Step 4: Run focused tests**

~~~bash
cd backend
go test ./internal/adapters/agent/hermes ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall -count=1
~~~

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/hermes backend/internal/adapters/agent/modelcatalog backend/internal/service/systeminstall
git commit -m "feat: report Hermes readiness and models"
~~~

### Task 5: Bind Hermes ACP without registering it

**Files:**
- Create: backend/internal/adapters/chatdriver/hermesacp/driver.go
- Create: backend/internal/adapters/chatdriver/hermesacp/driver_test.go
- Create: backend/internal/adapters/chatdriver/hermesacp/conformance_test.go

**Interfaces:**
- Consumes: nativeacp.New, hermes.Plugin, hermes MinimumVersion, and the per-process system-prompt-file contract.
- Produces: hermesacp.New(nativeacp.Plugin, *slog.Logger) ports.ChatDriver.
- Produces: launch argv hermes acp plus exact model/permission arguments and AO-owned prompt path.
- Does not produce: a Chat registry entry until every conformance case passes.

- [ ] **Step 1: Write failing binding tests**

~~~go
func TestConfigureUsesHermesACPAndStandingPromptFile(t *testing.T) {
	// Assert argv starts with acp, carries the exact prompt-file flag,
	// forwards model, and maps only explicitly supported permission modes.
}

func TestDriverRequiresLoadReplayPermissionsAndCancel(t *testing.T) {
	// A fake ACP server missing any required capability must make Probe fail.
}
~~~

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/chatdriver/hermesacp -count=1
~~~

- [ ] **Step 3: Implement the thin native ACP binding**

Use nativeacp.New. Enable only capabilities observed in the pinned fixture: usage, diffs, plans, attachments, reasoning, and configuration selectors must each have an assertion before being advertised. Preserve human approval by default; bypass is admitted only if the pinned Hermes release exposes an unambiguous process/session mode.

- [ ] **Step 4: Exercise provider restart and replay**

The conformance test must create a session, complete two turns with tool and plan events, park one permission, cancel one turn, kill only the provider process, resume by exact ACP session ID, and assert replay produces no duplicate AO messages. Then reconnect the detached AO host and assert the provider PID and request correlation are preserved.

~~~bash
cd backend
AO_HERMES_CONFORMANCE_BINARY=/absolute/path/to/hermes \
  go test ./internal/adapters/chatdriver/hermesacp -run TestHermesACPConformance -count=1 -v
go test ./internal/adapters/chatdriver/acp ./internal/adapters/chatdriver/persistenthost -count=1
~~~

Expected: PASS. Any missing session/load, replay, permission, cancellation, or reconnect behavior is a registration stop.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/chatdriver/hermesacp
git commit -m "feat: bind Hermes ACP chat driver"
~~~

### Task 6: Register the production harness and widen persistence/API contracts

**Files:**
- Modify: backend/internal/domain/harness.go
- Modify: backend/internal/domain/harness_test.go
- Modify: backend/internal/adapters/agent/registry/registry.go
- Modify: backend/internal/adapters/agent/registry/registry_test.go
- Modify: backend/internal/adapters/chatdriver/registry/registry.go
- Modify: backend/internal/adapters/chatdriver/registry/registry_test.go
- Create: backend/internal/storage/sqlite/migrations/0148_allow_hermes_harness.sql
- Create: backend/internal/storage/sqlite/migrate_hermes_harness_test.go
- Modify: backend/internal/storage/sqlite/migrate_burned_versions_test.go
- Modify: backend/internal/httpd/controllers/dto.go
- Modify: backend/internal/httpd/apispec/openapi.yaml
- Modify: frontend/src/api/schema.ts

**Interfaces:**
- Produces: domain.HarnessHermes = "hermes" in domain.AllHarnesses.
- Produces: one agent registry constructor and one Chat registry driver.
- Preserves: reviewer harness types and registries byte-for-byte.

- [ ] **Step 1: Write failing registry, migration, and API tests**

Assert Hermes is known, appears exactly once in the agent registry, supports Chat, migrates old databases forward and backward without data loss, and appears in worker/orchestrator/session enums. Assert reviewer enums and reviewer registry counts are unchanged.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/domain ./internal/adapters/agent/registry ./internal/adapters/chatdriver/registry ./internal/storage/sqlite ./internal/httpd/controllers -run 'Test.*Hermes' -count=1
~~~

- [ ] **Step 3: Add registration and migration**

Follow the table-rebuild pattern in 0083_reconcile_kimchi_prime_agent_harnesses.sql. The up migration adds hermes to the current complete CHECK list; the down migration removes only hermes. Do not edit an existing migration.

- [ ] **Step 4: Regenerate and verify the API**

~~~bash
npm run api
cd backend
go test ./internal/httpd/... ./internal/storage/sqlite/... -count=1
~~~

Expected: PASS with openapi.yaml and schema.ts changed together.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/domain backend/internal/adapters/agent/registry backend/internal/adapters/chatdriver/registry backend/internal/storage/sqlite/migrations/0148_allow_hermes_harness.sql backend/internal/storage/sqlite/migrate_hermes_harness_test.go backend/internal/storage/sqlite/migrate_burned_versions_test.go backend/internal/httpd frontend/src/api/schema.ts
git commit -m "feat: register Hermes agent and Chat"
~~~

### Task 7: Add the real brand asset and complete release verification

**Files:**
- Modify: packages/product-ui/src/agents.ts
- Create: packages/product-ui/src/agents.test.ts
- Create: frontend/src/renderer/assets/agents/hermes.svg
- Modify: frontend/src/renderer/components/AgentAvatar.tsx
- Modify: frontend/src/renderer/components/AgentAvatar.test.tsx
- Modify: docs/harnesses/hermes.md

**Interfaces:**
- Consumes: production readiness inventory and generated harness types.
- Produces: Hermes picker/avatar presentation using an upstream-authorized brand asset.
- Preserves: generic worker/orchestrator selection; no bespoke picker or reviewer UI.

- [ ] **Step 1: Add the failing avatar test**

~~~tsx
it("renders the Hermes brand asset", () => {
	render(<AgentAvatar provider="hermes" />);
	expect(screen.getByRole("img", { name: "hermes" })).toHaveAttribute(
		"src",
		expect.stringContaining("hermes.svg"),
	);
});
~~~

Create `packages/product-ui/src/agents.test.ts` and assert that `AGENT_OPTIONS` contains `hermes` exactly once and `AGENT_LABELS.hermes` equals `Hermes Agent`.

- [ ] **Step 2: Run the focused frontend test**

~~~bash
cd frontend
npm test -- --run src/renderer/components/AgentAvatar.test.tsx
~~~

Expected: FAIL because the asset mapping is absent.

- [ ] **Step 3: Add and document the asset**

Add `hermes` and its label to the shared product identity catalog. Use the official Hermes mark from https://github.com/NousResearch/hermes-agent and record its source path, upstream commit, and license in docs/harnesses/hermes.md. Add only the AgentAvatar mapping.

- [ ] **Step 4: Run all required verification**

~~~bash
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm test -- --run packages/product-ui/src/agents.test.ts frontend/src/renderer/components/AgentAvatar.test.tsx
npm run frontend:typecheck
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
~~~

Expected: every locally available job passes. Record unavailable authenticated/provider/platform jobs as exact gaps and verify them in CI; do not publish.

- [ ] **Step 5: Audit exclusions and registration truth**

~~~bash
git diff --name-only
git diff -- backend/internal/adapters/reviewer backend/internal/domain/reviewerharness.go
rg -n "hermes" backend/internal/adapters/chatdriver/registry/registry.go backend/internal/adapters/agent/registry/registry.go backend/internal/httpd/controllers/dto.go
~~~

Expected: no reviewer diff; Hermes is registered only after Tasks 1, 3, and 5 passed against the pinned release; no AgentInterfaceHandoff implementation exists.

- [ ] **Step 6: Commit**

~~~bash
git add packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts frontend/src/renderer/assets/agents/hermes.svg frontend/src/renderer/components/AgentAvatar.tsx frontend/src/renderer/components/AgentAvatar.test.tsx docs/harnesses/hermes.md
git commit -m "feat: finish Hermes agent integration"
~~~
