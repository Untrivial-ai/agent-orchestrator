# Amp, Pi, and Auggie Kanban Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Amp, Pi, and Auggie sessions report normalized live activity and native restore IDs to AO's Kanban pipeline.

**Architecture:** Each adapter installs the smallest native workspace integration supported by its CLI and emits JSON into `ao hooks <agent> <event>`. Adapter-local derivers translate native events into the existing AO activity states, while the shared dispatch registry makes all three harnesses signal-capable.

**Tech Stack:** Go 1.x adapters and tests, generated TypeScript plugin/extension source, JSON hook configuration, POSIX shell or Windows command wrappers.

## Global Constraints

- Keep the daemon's status derivation unchanged; status remains derived from durable activity facts.
- Preserve user-owned plugins, hooks, and unrelated JSON fields.
- Hook callbacks are best-effort and must never break or block the coding agent beyond a bounded timeout.
- Do not change API DTOs, OpenAPI artifacts, or frontend status vocabulary.
- Use test-first red-green cycles for every production behavior.

---

### Task 1: Amp lifecycle plugin

**Files:**
- Modify: `backend/internal/adapters/agent/amp/hooks.go`
- Create: `backend/internal/adapters/agent/amp/activity.go`
- Modify: `backend/internal/adapters/agent/amp/amp.go`
- Modify: `backend/internal/adapters/agent/amp/amp_test.go`
- Create: `backend/internal/adapters/agent/amp/activity_test.go`

**Interfaces:**
- Consumes: Amp `session.start`, `agent.start`, `agent.end`, and `PluginThread.state` events.
- Produces: `amp.DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool)` and `ao hooks amp ...` callbacks.

- [ ] **Step 1: Add failing Amp derivation tests**

```go
func TestDeriveActivityState(t *testing.T) {
    tests := []struct{ event, payload string; want domain.ActivityState; ok bool }{
        {"session-start", `{}`, "", false},
        {"user-prompt-submit", `{}`, domain.ActivityActive, true},
        {"stop", `{}`, domain.ActivityIdle, true},
        {"thread-state", `{"state":"running"}`, domain.ActivityActive, true},
        {"thread-state", `{"state":"idle"}`, domain.ActivityIdle, true},
        {"thread-state", `{"state":"awaiting-approval"}`, domain.ActivityWaitingInput, true},
        {"thread-state", `{"state":"error"}`, domain.ActivityWaitingInput, true},
    }
}
```

- [ ] **Step 2: Run the Amp tests and confirm they fail because `DeriveActivityState` is missing**

Run: `cd backend && go test ./internal/adapters/agent/amp`

- [ ] **Step 3: Implement the Amp deriver and extend generated plugin source**

Add payload-aware `thread-state` mapping. Extend the existing plugin with a bounded `Bun.spawnSync` reporter, native thread-ID payloads, one state subscription per thread, and the existing hidden system-prompt return from `agent.start`.

- [ ] **Step 4: Add failing plugin-source and `SessionInfo` tests**

Assert the generated source contains `session.start`, `agent.end`, `thread.state.subscribe`, `ao hooks amp`, bounded timeout handling, and the original hidden prompt injection. Assert `SessionInfo` returns `agentSessionId` through `agentbase.StandardSessionInfo`.

- [ ] **Step 5: Run Amp tests until green**

Run: `cd backend && go test ./internal/adapters/agent/amp`

### Task 2: Pi lifecycle extension

**Files:**
- Modify: `backend/internal/adapters/agent/pi/pi.go`
- Create: `backend/internal/adapters/agent/pi/hooks.go`
- Create: `backend/internal/adapters/agent/pi/activity.go`
- Modify: `backend/internal/adapters/agent/pi/pi_test.go`
- Create: `backend/internal/adapters/agent/pi/activity_test.go`

**Interfaces:**
- Consumes: Pi `session_start`, `before_agent_start`, `agent_end`, `agent_settled`, and `session_shutdown` extension events.
- Produces: `.pi/extensions/ao-activity.ts`, explicit `--extension <path>` launch/restore arguments, and `pi.DeriveActivityState`.

- [ ] **Step 1: Add failing Pi hook-installation, launch, restore, session-info, and derivation tests**

Assert installation creates an AO-managed extension without changing foreign extension files; launch and restore argv contain `--extension <workspace>/.pi/extensions/ao-activity.ts`; event mapping is `session-start -> idle`, `user-prompt-submit -> active`, `stop -> idle`, and `session-end -> exited`.

- [ ] **Step 2: Run the Pi tests and confirm the new expectations fail**

Run: `cd backend && go test ./internal/adapters/agent/pi`

- [ ] **Step 3: Implement managed Pi extension installation and command wiring**

Write the extension atomically with an AO sentinel, gitignore only AO's file, explicitly load it on launch/restore, and refuse to overwrite a foreign file at the managed path.

- [ ] **Step 4: Implement Pi derivation and standard session metadata**

Add `DeriveActivityState` and delegate `SessionInfo` to `agentbase.StandardSessionInfo`.

- [ ] **Step 5: Run Pi tests until green**

Run: `cd backend && go test ./internal/adapters/agent/pi`

### Task 3: Auggie command hooks

**Files:**
- Modify: `backend/internal/adapters/agent/auggie/auggie.go`
- Create: `backend/internal/adapters/agent/auggie/hooks.go`
- Create: `backend/internal/adapters/agent/auggie/activity.go`
- Modify: `backend/internal/adapters/agent/auggie/auggie_test.go`
- Create: `backend/internal/adapters/agent/auggie/activity_test.go`

**Interfaces:**
- Consumes: Auggie matcher-group hooks in `.augment/settings.local.json` and JSON payloads containing `conversation_id` and `agent_stop_cause`.
- Produces: executable `.augment/ao-hooks/ao-<event>.sh` or `.cmd` wrappers and `auggie.DeriveActivityState`.

- [ ] **Step 1: Add failing Auggie installation and derivation tests**

Assert AO hooks are merged without losing unrelated settings/user hooks; wrapper scripts are executable on Unix; repeated installation is idempotent; and Stop payload causes map correctly.

- [ ] **Step 2: Run the Auggie tests and confirm the new expectations fail**

Run: `cd backend && go test ./internal/adapters/agent/auggie`

- [ ] **Step 3: Implement wrapper generation and hooks-json reconciliation**

Use `hooksjson.Manager` with workspace-specific absolute commands for `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, and `SessionEnd`. Write scripts atomically and ignore only AO-managed files.

- [ ] **Step 4: Implement Auggie derivation and standard session metadata**

Map active, idle, attention-required, and exited states; delegate `SessionInfo` to `agentbase.StandardSessionInfo` so `conversation_id` becomes the native restore ID.

- [ ] **Step 5: Run Auggie tests until green**

Run: `cd backend && go test ./internal/adapters/agent/auggie`

### Task 4: Shared dispatch and regression verification

**Files:**
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch.go`
- Modify: `backend/internal/adapters/agent/activitydispatch/dispatch_test.go`

**Interfaces:**
- Consumes: the three adapter-local `DeriveActivityState` functions.
- Produces: signal-capable dispatch entries for `amp`, `pi`, and `auggie`.

- [ ] **Step 1: Add failing dispatch tests for all three harnesses**

Extend `TestSupportsHarness` and dispatch cases so Amp, Pi, and Auggie must be supported and their representative events derive the expected state.

- [ ] **Step 2: Run dispatch tests and confirm they fail for missing registry entries**

Run: `cd backend && go test ./internal/adapters/agent/activitydispatch`

- [ ] **Step 3: Register all three derivers**

Import `amp`, `pi`, and `auggie` and add their exact harness tokens to `activitydispatch.Derivers`.

- [ ] **Step 4: Run focused regression suites**

Run: `cd backend && go test ./internal/adapters/agent/amp ./internal/adapters/agent/pi ./internal/adapters/agent/auggie ./internal/adapters/agent/activitydispatch ./internal/cli ./internal/service/session ./internal/session_manager ./internal/adapters/agent/registry`

- [ ] **Step 5: Run repository verification**

Run: `npm run lint`

Run: `npm run frontend:typecheck`

- [ ] **Step 6: Review the final diff and commit**

Run: `git diff --check`

Run: `git status --short`

Commit: `feat: add Amp Pi and Auggie activity hooks`
