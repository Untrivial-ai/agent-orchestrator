# DeepAgents Agent Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Add a production DeepAgents Code integration for AO workers and orchestrators with exact thread restore, durable lifecycle facts, safe standing instructions, and a conformant ACP Chat driver.

**Architecture:** Treat dcode as the user-owned harness and keep AO as a thin supervisor. A DeepAgents agent package owns executable/auth/model/TUI behavior and an AO-only hook file; a thin deepagentsacp package reuses AO's persistent ACP transport. Production registration is the last step and is blocked until a pinned upstream release proves an append-system-prompt file or non-destructive profile overlay, an isolated hook-config input, and the complete ACP load/replay/permission/cancellation contract.

**Tech Stack:** Go 1.25, DeepAgents Code 0.1.70 or newer, Python 3.12+, LangGraph SQLite checkpoints, Agent Client Protocol, SQLite migrations, OpenAPI, React/TypeScript, Vitest.

**Spec:** docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md

## Global Constraints

- The canonical harness ID is deepagents.
- The same adapter serves worker and orchestrator roles; AO owns role prompt construction.
- Never set DEEPAGENTS_HOME to an AO-only directory unless the pinned upstream contract proves that user auth, config, sessions, and trust state remain inherited without copying secrets.
- Never write ~/.deepagents/AGENTS.md, ~/.deepagents/hooks.json, project .deepagents files, or any user-owned agent profile.
- Never concatenate AO standing instructions into the visible first user message.
- Initial interactive work uses -m/--message; -n/--non-interactive is prohibited because it always creates a fresh thread and exits.
- Restore always uses -r with the exact UUID7 thread ID; bare -r and most-recent selection are prohibited.
- dcode --auto-approve is classifier-backed Auto, not an unconditional approval mode. dcode --yolo is used only for AO bypass after the upstream acknowledgement contract is satisfied.
- Hooks only observe and report. They never return exit 2, permissionDecision, continue=false, or any stdout that changes agent behavior.
- TUI-to-Chat handoff is excluded. Do not implement ports.AgentInterfaceHandoff.
- Reviewer adapters, reviewer enums, reviewer assets, and reviewer UI are excluded.
- If the system-prompt/profile-overlay gate fails, stop before production registration.
- If isolated hooks/native-ID capture fail, do not register TUI support.
- If ACP session/load, replay, permissions, cancellation, or reconnect fail, do not register Chat support.

---

### Task 1: Pin and execute the DeepAgents upstream conformance contract

**Files:**
- Create: backend/internal/adapters/agent/deepagents/contract.go
- Create: backend/internal/adapters/agent/deepagents/contract_test.go
- Create: backend/internal/adapters/agent/deepagents/upstream_conformance_test.go
- Create: backend/internal/adapters/agent/deepagents/testdata/session-start.json
- Create: docs/harnesses/deepagents.md

**Interfaces:**
- Produces: deepagents.Contract describing Version, SystemPromptFile, IsolatedHooks, InitialMessage, ExactRestore, TUISessionID, ACP, ACPLoad, ACPReplay, ACPPermissions, ACPCancel, and AuthStatus.
- Produces: ValidateContract(Contract) error and MinimumVersion.
- Consumes: AO_DEEPAGENTS_CONFORMANCE_BINARY for a real dcode/deepagents-code executable.

- [ ] **Step 1: Write failing contract tests**

~~~go
func TestValidateContractBlocksProfileReplacement(t *testing.T) {
	err := ValidateContract(Contract{
		Version: "0.1.70", ACP: true, UsesReplacementHome: true,
	})
	if !errors.Is(err, ErrProfileIsolationUnsafe) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateContractRequiresPromptAndHooks(t *testing.T) {
	err := ValidateContract(Contract{Version: "0.1.70", ACP: true, ACPLoad: true})
	if !errors.Is(err, ErrSystemPromptContractMissing) {
		t.Fatalf("error = %v", err)
	}
}
~~~

The live test must create a temporary workspace and AO data directory while retaining a disposable DeepAgents profile containing fake credentials/config/session fixtures. It must assert:

1. Version is at least 0.1.70 and Python satisfies >=3.12,<4.
2. A per-process --system-prompt-file path, or a documented overlay that reads only the AO file while inheriting the original profile, adds AO instructions after the built-in dcode system prompt in fresh TUI, exact restore, fresh ACP, and ACP load.
3. The AO prompt bytes do not appear as a HumanMessage and no user/profile/project file changes.
4. A per-process --hooks-file path loads an AO-owned Hooks v2 document without changing or shadowing user/project/plugin hooks.
5. SessionStart exposes the exact UUID7 thread ID; UserPromptSubmit, PermissionRequest, Stop, and SessionEnd fire with that same ID.
6. dcode -m TASK stays interactive on a PTY; dcode -r THREAD_ID loads exactly that thread and reapplies model, permission, prompt, and hook flags.
7. dcode --acp supports initialize, session/new, prompt streaming, permission response, cancellation, process restart, session/load, and history replay.
8. dcode auth status PROVIDER distinguishes missing/configured/implicit/unknown locally without validating a key by presence alone.

- [ ] **Step 2: Run the conformance tests**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents -run TestValidateContract -count=1
AO_DEEPAGENTS_CONFORMANCE_BINARY=/absolute/path/to/dcode \
  go test ./internal/adapters/agent/deepagents -run TestDeepAgentsUpstreamConformance -count=1 -v
~~~

Expected against 0.1.70 if no new overlay flags exist: fail with ErrSystemPromptContractMissing or ErrHookIsolationMissing. Stop; do not execute Tasks 2-7 and do not add deepagents to registries, migrations, DTO enums, or UI.

- [ ] **Step 3: Implement the contract validator**

~~~go
type Contract struct {
	Version             string
	SystemPromptFile    bool
	SupportedOverlay    bool
	UsesReplacementHome bool
	IsolatedHooks       bool
	InitialMessage      bool
	ExactRestore        bool
	TUISessionID        bool
	ACP                 bool
	ACPLoad             bool
	ACPReplay           bool
	ACPPermissions      bool
	ACPCancel           bool
	AuthStatus          bool
}

func ValidateContract(c Contract) error
~~~

Use a package-local three-component semantic-version parser following backend/internal/adapters/chatdriver/kimchiacp/version.go, so this gate adds no dependency. Once the live suite passes, set MinimumVersion to its exact dcode version and record the executable hash, effective profile paths, tested OSes, fake provider transcript, and accepted prompt/hook mechanism in docs/harnesses/deepagents.md.

- [ ] **Step 4: Re-run package tests**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents -count=1
~~~

Expected: PASS and MinimumVersion is not lower than 0.1.70.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/deepagents docs/harnesses/deepagents.md
git commit -m "test: pin DeepAgents adapter contract"
~~~

### Task 2: Implement deterministic DeepAgents TUI launch and restore

**Files:**
- Create: backend/internal/adapters/agent/deepagents/deepagents.go
- Create: backend/internal/adapters/agent/deepagents/deepagents_test.go
- Create: backend/internal/adapters/agent/deepagents/install.go
- Create: backend/internal/adapters/agent/deepagents/install_test.go

**Interfaces:**
- Consumes: ports.Agent, ports.AgentBinaryResolver, ports.AgentExitDetector, and the prompt/hook mechanism accepted by Task 1.
- Produces: deepagents.New() *Plugin.
- Produces: ResolveDeepAgentsBinary(context.Context) (string, error).
- Produces: exact fresh and restore argv without shell interpolation.

- [ ] **Step 1: Write failing table-driven command tests**

~~~go
func TestLaunchCommand(t *testing.T) {
	tests := []struct {
		name string
		cfg  ports.LaunchConfig
		want []string
	}{
		{"initial task", launchConfig("repair the parser"), []string{
			"/resolved/dcode", "-m", "repair the parser",
			"--system-prompt-file", "/ao/system.md",
			"--hooks-file", "/ao/hooks.json",
		}},
		{"model and auto", launchConfigWith("openai:gpt-5.5", ports.PermissionModeAuto), []string{
			"/resolved/dcode", "-m", "repair the parser",
			"--system-prompt-file", "/ao/system.md",
			"--hooks-file", "/ao/hooks.json",
			"--model", "openai:gpt-5.5", "--auto-approve",
		}},
	}
	// Assert exact argv.
}
~~~

Add cases for the upstream overlay form if Task 1 selected it, exact -r UUID restore, missing session ID, absent prompt/hook files, --yolo acknowledgement failure, unsupported accept-edits, cancellation, dcode/deepagents-code fallback order, and Windows shims.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents -run 'Test(Launch|Restore|Resolve)' -count=1
~~~

- [ ] **Step 3: Implement the adapter**

~~~go
func (p *Plugin) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error)
func (p *Plugin) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error)
func (p *Plugin) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error)
func (p *Plugin) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error)
func (p *Plugin) ResolveBinary(context.Context) (string, error)
func (p *Plugin) ExitDetectionMode() ports.AgentExitDetectionMode
~~~

Use PromptDeliveryInCommand because -m is a documented initial auto-submit channel. Forward a model as --model. Leave manual as the default. Map AO auto only to --auto-approve, bypass only to --yolo, and return a validation error for accept-edits unless the conformance record proves a semantic equivalent. Restore must reapply every flag and use agentbase.StandardSessionInfo.

- [ ] **Step 4: Run adapter tests**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/deepagents
git commit -m "feat: add DeepAgents terminal adapter"
~~~

### Task 3: Install an isolated Hooks v2 observer and capture thread identity

**Files:**
- Create: backend/internal/adapters/agent/deepagents/hooks.go
- Create: backend/internal/adapters/agent/deepagents/hooks_test.go
- Create: backend/internal/adapters/agent/deepagents/activity.go
- Create: backend/internal/adapters/agent/deepagents/activity_test.go
- Create: backend/internal/adapters/agent/deepagents/assets/ao-hooks.json
- Create: backend/internal/adapters/agent/deepagents/assets/ao_hook.py
- Modify: backend/internal/adapters/agent/activitydispatch/dispatch.go
- Modify: backend/internal/adapters/agent/activitydispatch/dispatch_test.go

**Interfaces:**
- Consumes: the isolated --hooks-file contract proven in Task 1 and AO_SESSION_ID.
- Produces: DATA_DIR/agent-runtime/deepagents/hooks.json and ao_hook.py with mode 0600.
- Produces: DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool).
- Produces: agentSessionId metadata from SessionStart's exact thread ID.

- [ ] **Step 1: Write failing lifecycle mapping tests**

~~~go
func TestDeriveActivityState(t *testing.T) {
	tests := []struct{ event string; want domain.ActivityState }{
		{"SessionStart", domain.ActivityIdle},
		{"UserPromptSubmit", domain.ActivityActive},
		{"PreToolUse", domain.ActivityActive},
		{"PermissionRequest", domain.ActivityBlocked},
		{"PostToolUse", domain.ActivityActive},
		{"PostToolUseFailure", domain.ActivityActive},
		{"Stop", domain.ActivityIdle},
		{"SessionEnd", domain.ActivityExited},
	}
	// Assert exact mapping; unrecognized input returns ok=false.
}
~~~

Test atomic writes, repeat idempotence, cancellation, bounded JSON, Windows argv invocation, no workspace/profile writes, user-hook coexistence in the live fixture, and no behavior-changing output or exit code.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents ./internal/adapters/agent/activitydispatch -run 'Test(DeepAgents|DeriveActivity|GetAgentHooks)' -count=1
~~~

- [ ] **Step 3: Implement AO-owned hook assets**

Hooks v2 must register SessionStart, UserPromptSubmit, PermissionRequest, PreToolUse, PostToolUse, PostToolUseFailure, Stop, and SessionEnd. Use direct-exec argv rather than shell-form command. The handler reads one bounded JSON object from stdin and invokes ao hooks deepagents EVENT with the native thread ID in the standard metadata field. It prints nothing and always exits zero after best-effort observation.

~~~go
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)
~~~

- [ ] **Step 4: Run focused tests**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents ./internal/adapters/agent/activitydispatch -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/deepagents backend/internal/adapters/agent/activitydispatch
git commit -m "feat: observe DeepAgents session activity"
~~~

### Task 4: Add installation, authentication, and model readiness

**Files:**
- Create: backend/internal/adapters/agent/deepagents/auth.go
- Create: backend/internal/adapters/agent/deepagents/auth_test.go
- Modify: backend/internal/adapters/agent/deepagents/deepagents.go
- Modify: backend/internal/adapters/agent/modelcatalog/catalog.go
- Modify: backend/internal/adapters/agent/modelcatalog/catalog_test.go
- Modify: backend/internal/service/systeminstall/agentplans.go
- Modify: backend/internal/service/systeminstall/agentplans_test.go

**Interfaces:**
- Consumes: dcode auth status PROVIDER and ACP session configuration options.
- Produces: ports.AgentAuthChecker with presence-only results classified as configured.
- Produces: model selection using provider:model IDs; ACP-discovered options when available, direct text fallback otherwise.
- Produces: install guidance for the official https://langch.in/dcode installer without executing a remote script automatically.

- [ ] **Step 1: Write failing readiness tests**

Cover missing executable => unavailable; auth labels stored/env => configured; missing => unauthorized; implicit/no-key-required/custom => unknown unless a provider round trip passes; corrupt output, stderr warnings, timeout, cancellation, secret redaction; provider:model validation; and ACP model option normalization.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall -run 'Test(DeepAgents|deepagents)' -count=1
~~~

- [ ] **Step 3: Implement bounded local probes**

Resolve the effective provider from the configured model without reading secret values. Invoke dcode auth status with that provider and parse only the contract recorded in Task 1. Never report authorized solely because auth.json or an environment variable exists. For model discovery, prefer ACP config options returned by a no-prompt session/new and close the short-lived process; otherwise expose ModelSelectionText with CustomModelEntryDirect.

- [ ] **Step 4: Run readiness tests**

~~~bash
cd backend
go test ./internal/adapters/agent/deepagents ./internal/adapters/agent/modelcatalog ./internal/service/systeminstall -count=1
~~~

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/agent/deepagents backend/internal/adapters/agent/modelcatalog backend/internal/service/systeminstall
git commit -m "feat: report DeepAgents readiness and models"
~~~

### Task 5: Implement and prove the DeepAgents ACP binding

**Files:**
- Create: backend/internal/adapters/chatdriver/deepagentsacp/driver.go
- Create: backend/internal/adapters/chatdriver/deepagentsacp/driver_test.go
- Create: backend/internal/adapters/chatdriver/deepagentsacp/conformance_test.go

**Interfaces:**
- Consumes: nativeacp.New, deepagents.Plugin, MinimumVersion, and the standing-prompt contract.
- Produces: deepagentsacp.New(nativeacp.Plugin, *slog.Logger) ports.ChatDriver.
- Produces: dcode --acp launch with model, permission, system-prompt, and isolated-hook inputs.
- Does not produce: a registry entry until live conformance passes.

- [ ] **Step 1: Write failing configuration tests**

~~~go
func TestConfigureUsesACPAndReappliesStandingPrompt(t *testing.T) {
	// Assert --acp, the proven system-prompt and hook inputs, model forwarding,
	// manual default, --auto-approve for AO auto, and --yolo only for bypass.
}

func TestCapabilitiesAreEvidenceBacked(t *testing.T) {
	// Assert omitted attachment/audio/structured-input capabilities stay false
	// unless the pinned ACP initialize response and projection tests prove them.
}
~~~

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/adapters/chatdriver/deepagentsacp -count=1
~~~

- [ ] **Step 3: Implement the native ACP wrapper**

Use nativeacp.New and exact dcode argv. Treat ACP session ID as the native LangGraph thread ID only because the live test proves equality. Map model through the advertised model selector. A free-form LangGraph interrupt is unsupported; only action_requests/review permission shapes may reach AO. Keep all permissions human-blocked unless the requested AO mode has an exact tested mapping.

- [ ] **Step 4: Run complete ACP conformance**

The live test must initialize, create a cwd-bound session, submit two turns, observe assistant/reasoning/tool/todo events, answer approve and reject requests, cancel an active turn, restart dcode, load the exact session, restore mode/model, and replay history without duplicate messages. It must then reconnect AO's detached host and prove PID/request-correlation preservation.

~~~bash
cd backend
AO_DEEPAGENTS_CONFORMANCE_BINARY=/absolute/path/to/dcode \
  go test ./internal/adapters/chatdriver/deepagentsacp -run TestDeepAgentsACPConformance -count=1 -v
go test ./internal/adapters/chatdriver/acp ./internal/adapters/chatdriver/persistenthost -count=1
~~~

Expected: PASS. A cwd mismatch must be a typed resume failure, not a fresh session. Any missing load/replay/permission/cancel behavior is a registration stop.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/adapters/chatdriver/deepagentsacp
git commit -m "feat: bind DeepAgents ACP chat driver"
~~~

### Task 6: Register DeepAgents in domain, storage, API, and Chat

**Files:**
- Modify: backend/internal/domain/harness.go
- Modify: backend/internal/domain/harness_test.go
- Modify: backend/internal/adapters/agent/registry/registry.go
- Modify: backend/internal/adapters/agent/registry/registry_test.go
- Modify: backend/internal/adapters/chatdriver/registry/registry.go
- Modify: backend/internal/adapters/chatdriver/registry/registry_test.go
- Create: backend/internal/storage/sqlite/migrations/0148_allow_deepagents_harness.sql
- Create: backend/internal/storage/sqlite/migrate_deepagents_harness_test.go
- Modify: backend/internal/storage/sqlite/migrate_burned_versions_test.go
- Modify: backend/internal/httpd/controllers/dto.go
- Modify: backend/internal/httpd/apispec/openapi.yaml
- Modify: frontend/src/api/schema.ts

**Interfaces:**
- Produces: domain.HarnessDeepAgents = "deepagents".
- Produces: one production Agent constructor and one Chat driver registration.
- Preserves: every reviewer enum and reviewer registry unchanged.

- [ ] **Step 1: Write failing registration tests**

Assert deepagents is known and occurs once in AllHarnesses, once in agent constructors, once in Chat harnesses, and in worker/orchestrator/session API enums. Assert migration up/down preserves existing rows and changes only the sessions.harness CHECK. Snapshot reviewer enum values and reviewer constructor count before the edit and assert equality afterward.

- [ ] **Step 2: Run tests and confirm failure**

~~~bash
cd backend
go test ./internal/domain ./internal/adapters/agent/registry ./internal/adapters/chatdriver/registry ./internal/storage/sqlite ./internal/httpd/controllers -run 'Test.*DeepAgents' -count=1
~~~

- [ ] **Step 3: Add the harness only after all gates pass**

Follow the current table-rebuild migration pattern. The up migration adds deepagents to the full current CHECK set; the down migration removes only deepagents. Add deepagents.New() to the agent registry and deepagentsacp.New(deepagents.New(), log) to Chat. Do not add AgentInterfaceHandoff.

- [ ] **Step 4: Regenerate API artifacts**

~~~bash
npm run api
cd backend
go test ./internal/httpd/... ./internal/storage/sqlite/... -count=1
~~~

Expected: PASS and generated OpenAPI/TypeScript files agree.

- [ ] **Step 5: Commit**

~~~bash
git add backend/internal/domain backend/internal/adapters/agent/registry backend/internal/adapters/chatdriver/registry backend/internal/storage/sqlite/migrations/0148_allow_deepagents_harness.sql backend/internal/storage/sqlite/migrate_deepagents_harness_test.go backend/internal/storage/sqlite/migrate_burned_versions_test.go backend/internal/httpd frontend/src/api/schema.ts
git commit -m "feat: register DeepAgents agent and Chat"
~~~

### Task 7: Add the brand asset, user-facing evidence, and final gates

**Files:**
- Modify: packages/product-ui/src/agents.ts
- Create: packages/product-ui/src/agents.test.ts
- Create: frontend/src/renderer/assets/agents/deepagents.svg
- Modify: frontend/src/renderer/components/AgentAvatar.tsx
- Modify: frontend/src/renderer/components/AgentAvatar.test.tsx
- Modify: docs/harnesses/deepagents.md

**Interfaces:**
- Consumes: backend readiness inventory and regenerated DeepAgents types.
- Produces: a real upstream-authorized DeepAgents logo and complete capability documentation.
- Preserves: generic agent picker behavior; no custom reviewer or picker.

- [ ] **Step 1: Add the failing avatar test**

~~~tsx
it("renders the DeepAgents brand asset", () => {
	render(<AgentAvatar provider="deepagents" />);
	expect(screen.getByRole("img", { name: "deepagents" })).toHaveAttribute(
		"src",
		expect.stringContaining("deepagents.svg"),
	);
});
~~~

Create `packages/product-ui/src/agents.test.ts` and assert that `AGENT_OPTIONS` contains `deepagents` exactly once and `AGENT_LABELS.deepagents` equals `DeepAgents`.

- [ ] **Step 2: Run the focused test**

~~~bash
cd frontend
npm test -- --run src/renderer/components/AgentAvatar.test.tsx
~~~

Expected: FAIL because the logo mapping is absent.

- [ ] **Step 3: Add and document the asset**

Add `deepagents` and its label to the shared product identity catalog. Use the official mark from https://github.com/langchain-ai/deepagents and record the upstream path, commit, and MIT license in docs/harnesses/deepagents.md. Document the pinned dcode/deepagents-acp versions, Python floor, exact prompt/hook flags or overlay, permission mapping, auth limitations, and unsupported free-form interrupts/audio.

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

Expected: every locally available job passes. Report authenticated provider, Windows, and other unavailable platform runs as named CI/manual gaps; do not use publishing as validation.

- [ ] **Step 5: Audit registration and exclusions**

~~~bash
git diff --name-only
git diff -- backend/internal/adapters/reviewer backend/internal/domain/reviewerharness.go
rg -n "deepagents" backend/internal/adapters/agent/registry/registry.go backend/internal/adapters/chatdriver/registry/registry.go backend/internal/httpd/controllers/dto.go
~~~

Expected: no reviewer diff; no user/profile/project configuration writes; no TUI-to-Chat handoff; production registration exists only after Tasks 1, 3, and 5 pass on the pinned release.

- [ ] **Step 6: Commit**

~~~bash
git add packages/product-ui/src/agents.ts packages/product-ui/src/agents.test.ts frontend/src/renderer/assets/agents/deepagents.svg frontend/src/renderer/components/AgentAvatar.tsx frontend/src/renderer/components/AgentAvatar.test.tsx docs/harnesses/deepagents.md
git commit -m "feat: finish DeepAgents integration"
~~~
