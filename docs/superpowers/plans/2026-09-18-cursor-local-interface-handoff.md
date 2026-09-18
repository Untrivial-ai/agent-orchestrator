# Cursor Local TUI–Chat Interface Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely enable bidirectional local Cursor TUI–Chat switching only when the installed Cursor CLI proves stable native identity, trusted completed-turn boundaries, and complete ACP history replay.

**Architecture:** Keep the existing Session Manager transition saga and local `cursor-agent acp` Chat driver. First add and run a real ACP → interactive TUI → ACP → interactive TUI conformance test. Only after that gate passes, add the small Cursor handoff capability, provider-state probe, checkpoint verifier, hook facts, and coordinator regression coverage needed by the existing fail-closed architecture.

**Tech Stack:** Go 1.24, Cursor Agent CLI/ACP, `github.com/creack/pty`, existing AO adapter/ports/session-manager test infrastructure.

**Spec:** `docs/superpowers/specs/2026-09-18-cursor-local-interface-handoff-design.md`

## Global Constraints

- Use an isolated git worktree because the invoking checkout contains unrelated user changes.
- Use only the user's installed Cursor binary; do not download or bundle Cursor.
- Keep all test/provider state under an isolated temporary `AO_DATA_DIR`; never write to or import `~/.cursor`.
- Do not copy the cloud headless Chat runner or PR #4416's transcript/LCS reconciliation layer.
- Do not change the generic transition state machine unless a failing generic regression proves a provider-neutral defect.
- Terminal output is test evidence only, never canonical history or a continuation prompt.
- Cursor stays excluded from interface switching unless every conformance assertion passes.
- If Cursor lacks a stable native turn/message identifier or ACP omits a TUI turn, stop after Task 1 and report the provider limitation.
- The current locally installed research version is `cursor-agent 2026.09.02-c22c1a3`; record the exact version exercised by the live gate.

## File Map

- Create `backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go`: opt-in real-provider cross-interface conformance test and PTY driver.
- Create `backend/internal/adapters/chatdriver/cursoracp/checkpoint.go`: Cursor native checkpoint verification, only if Task 1 proves a stable provider identifier.
- Create `backend/internal/adapters/chatdriver/cursoracp/checkpoint_test.go`: bounded parser/verifier tests using sanitized fixtures captured by Task 1.
- Create `backend/internal/adapters/agent/cursor/continuation.go`: native-id mapping and AO-owned provider-history existence probe.
- Modify `backend/internal/adapters/agent/cursor/cursor.go`: declare the two existing handoff capability interfaces.
- Modify `backend/internal/adapters/agent/cursor/cursor_test.go`: native-id and history-probe tests.
- Modify `backend/internal/adapters/chatdriver/cursoracp/driver.go`: wrap the ACP driver with `NativeCheckpointVerifier`.
- Modify `backend/internal/cli/hooks.go`: admit only the Cursor hook fields proven by Task 1 as trusted conversation facts.
- Modify `backend/internal/cli/hooks_test.go`: replace the non-switching Cursor regression with positive and malformed/stale fact cases while retaining a different non-switching harness case.
- Modify `backend/internal/session_manager/interface_transition_test.go`: prove Cursor uses the existing bidirectional saga and fails closed on identity/history mismatches.
- Modify `docs/architecture.md` and `docs/STATUS.md`: describe support, minimum verified version/platform, and validation gaps.

---

### Task 1: Prove Cursor's Native Cross-Interface Contract

**Files:**
- Create: `backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go`
- Modify: `backend/internal/adapters/chatdriver/cursoracp/live_test.go`

**Interfaces:**
- Consumes: `New(cursor.New(), nil) ports.ChatDriver`, `ports.ChatHistoryReader`, `ports.ChatHistoryRefresher`, `cursor.Plugin.GetAgentHooks`, and `cursor.Plugin.GetRestoreCommand`.
- Produces: `TestLiveCursorInterfaceHandoff`, plus sanitized evidence stating the observed native-id field names and structured ACP history identities.

- [ ] **Step 1: Add the opt-in failing conformance test**

Create a Darwin/Unix live test guarded by `AO_LIVE_CURSOR_HANDOFF=1`. Reuse the existing live ACP helpers and add these assertions in this order:

```go
func TestLiveCursorInterfaceHandoff(t *testing.T) {
    if os.Getenv("AO_LIVE_CURSOR_HANDOFF") != "1" {
        t.Skip("set AO_LIVE_CURSOR_HANDOFF=1 to run the Cursor cross-interface contract")
    }
    // Use t.TempDir() for workspace and AO_DATA_DIR.
    // ACP turn A -> stop -> interactive --resume turn B -> stop.
    // ACP session/load must replay A and B exactly once with non-empty stable IDs.
    // ACP turn C -> stop -> interactive --resume must recall A, B, and C.
    // Every sessionStart hook must report the original ACP conversation id.
}
```

Drive the real interactive process with `pty.StartWithSize`. Install Cursor hooks in the temporary workspace and prepend a test-owned fake `ao` executable to `PATH`; that executable records hook event names and JSON to the temporary directory and returns `{}`. Never log prompt/answer bodies—assert unique random markers in memory and emit only field names, ids, counts, and the Cursor version on failure.

- [ ] **Step 2: Run the existing Cursor ACP live test**

Run:

```bash
cd backend
AO_DATA_DIR="$(mktemp -d)" AO_LIVE_CURSOR_ACP=1 go test ./internal/adapters/chatdriver/cursoracp -run '^TestLiveCursorACP$' -count=1 -v
```

Expected: PASS, proving the local account, binary, ACP resume, and isolated data root work before testing handoff.

- [ ] **Step 3: Run the new cross-interface test and enforce the hard gate**

Run:

```bash
cd backend
AO_LIVE_CURSOR_HANDOFF=1 go test ./internal/adapters/chatdriver/cursoracp -run '^TestLiveCursorInterfaceHandoff$' -count=1 -v
```

Expected to proceed: PASS with the same conversation id on every interface, both TUI and ACP turns replayed once, and non-empty stable provider identities.

If the test fails because ACP omits the TUI turn, the id changes, or Cursor exposes no stable turn/message identity, commit only the live test and evidence note, leave Cursor switching disabled, and stop this plan. Do not execute Tasks 2–6.

- [ ] **Step 4: Record sanitized provider evidence in the test comment**

Document the exact Cursor version, OS, session-start identity field, ACP replay identity fields, and bounded flush duration that passed. Do not commit native transcripts, account metadata, workspace paths, prompts, answers, or credentials.

- [ ] **Step 5: Run the package tests**

Run:

```bash
cd backend
go test ./internal/adapters/chatdriver/cursoracp
```

Expected: PASS with live tests skipped when their environment variables are absent.

- [ ] **Step 6: Commit the conformance harness**

```bash
git add backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go backend/internal/adapters/chatdriver/cursoracp/live_test.go
git commit -m "test: prove Cursor cross-interface continuity"
```

### Task 2: Add Cursor Native Identity and History Presence Capabilities

**Files:**
- Create: `backend/internal/adapters/agent/cursor/continuation.go`
- Modify: `backend/internal/adapters/agent/cursor/cursor.go`
- Modify: `backend/internal/adapters/agent/cursor/cursor_test.go`

**Interfaces:**
- Consumes: `ports.AgentInterfaceHandoff`, `ports.AgentInterfaceHandoffHistoryProbe`, `ports.MetadataKeyAgentSessionID`, and the Task 1-proven Cursor transcript location under `CURSOR_DATA_DIR/projects`.
- Produces: `(*Plugin).NativeConversationID(...) (string, bool, error)` and `(*Plugin).NativeConversationExists(...) (bool, error)`.

- [ ] **Step 1: Write failing identity tests**

Add table tests proving Chat uses the supplied provider id, TUI uses only `agentSessionId`, blanks return `ok=false`, and canceled contexts return the context error.

```go
id, ok, err := plugin.NativeConversationID(ctx, ports.SessionRef{
    Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "cursor-native-1"},
}, domain.SessionModeTUI, "")
```

Expected: `id == "cursor-native-1"`, `ok == true`, `err == nil`.

- [ ] **Step 2: Write failing AO-owned history-probe tests**

Construct `<data>/cursor/projects/<project>/agent-transcripts/<id>/<id>.jsonl` and verify that exactly one non-empty regular transcript returns true. Missing, empty, duplicate, symlink, invalid-id, and paths outside `CURSOR_DATA_DIR` return false or a bounded error without reading `~/.cursor`.

- [ ] **Step 3: Run the failing adapter tests**

Run:

```bash
cd backend
go test ./internal/adapters/agent/cursor -run 'TestNativeConversation|TestCursorNativeHistory' -count=1
```

Expected: FAIL because Cursor does not declare or implement the handoff interfaces.

- [ ] **Step 4: Implement the minimal capability**

Declare both interfaces in `cursor.go`. In `continuation.go`, trim ids, reject separators/glob characters, source TUI identity from `MetadataKeyAgentSessionID`, source Chat identity from `providerConversationID`, resolve the data root only from `env["CURSOR_DATA_DIR"]`, and require one non-empty regular transcript at:

```text
<CURSOR_DATA_DIR>/projects/*/agent-transcripts/<id>/<id>.jsonl
```

Return an ambiguity error when more than one match exists. Do not parse transcript messages in this component.

- [ ] **Step 5: Run and commit the adapter tests**

```bash
cd backend
go test ./internal/adapters/agent/cursor -count=1
git add internal/adapters/agent/cursor/continuation.go internal/adapters/agent/cursor/cursor.go internal/adapters/agent/cursor/cursor_test.go
git commit -m "feat: declare Cursor interface handoff identity"
```

Expected: PASS.

### Task 3: Add Trusted Cursor Hook Evidence and Native Checkpoint Verification

**Files:**
- Create: `backend/internal/adapters/chatdriver/cursoracp/checkpoint.go`
- Create: `backend/internal/adapters/chatdriver/cursoracp/checkpoint_test.go`
- Modify: `backend/internal/adapters/chatdriver/cursoracp/driver.go`
- Modify: `backend/internal/cli/hooks.go`
- Modify: `backend/internal/cli/hooks_test.go`

**Interfaces:**
- Consumes: `ports.NativeCheckpointVerifier`, `ports.NativeCheckpointRequest`, `domain.NativeCheckpointEvidence`, and only the Task 1-proven Cursor hook/native record fields.
- Produces: a Cursor driver implementing `VerifyNativeCheckpoint(context.Context, ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error)`.

- [ ] **Step 1: Add failing hook fact tests from sanitized live fixtures**

Use the exact field names observed in Task 1. Assert that main-thread `beforeSubmitPrompt` records a human submission with a stable provider turn/message id, `stop` records only the matching completed assistant boundary, subagent/foreign-id payloads are ignored, control characters and oversized ids are rejected, and malformed JSON produces empty facts.

Retain `TestHooks_NonSwitchingHarnessDoesNotReportConversationFacts`, but change its fixture harness from Cursor to an ACP harness that still lacks `AgentInterfaceHandoff`.

- [ ] **Step 2: Add failing checkpoint verifier tests**

Build sanitized provider records with the exact Task 1 schema and test:

```go
boundary, err := verifier.VerifyNativeCheckpoint(ctx, ports.NativeCheckpointRequest{
    ProviderConversationID: "cursor-native-1",
    Env: map[string]string{"CURSOR_DATA_DIR": dataDir},
    Evidence: encodedOwnedEvidence,
})
```

Cover matching completed ancestry, missing submission, pending tail, wrong native id, duplicate stable id, repeated identical text with different ids, partial record, oversized input, symlink/ambiguous transcript, canceled context, and coordination turns. Every unresolved case must wrap `ports.ErrChatHistoryUnsettled`; none may select a turn by text alone.

- [ ] **Step 3: Run the failing hook and verifier tests**

```bash
cd backend
go test ./internal/cli ./internal/adapters/chatdriver/cursoracp -run 'Cursor.*ConversationFacts|Cursor.*Checkpoint' -count=1
```

Expected: FAIL because Cursor facts and verifier are absent.

- [ ] **Step 4: Implement only the proven fields and bounds**

Extend `hookConversationFacts` and the allowlist switch for Cursor using the Task 1 field contract. Implement a focused checkpoint reader capped at 64 MiB with a 4 MiB maximum record, reject malformed/partial records, follow provider identity/ancestry rather than file order where ancestry exists, and return the final completed user boundary:

```go
ports.NativeCheckpointBoundary{
    UserMessageID: nativeUserID,
    UserText: userText,
    AssistantText: assistantText,
}
```

Wrap the existing Cursor ACP driver in a small `checkpointDriver`, matching the Claude driver pattern. Do not add terminal-history synthesis or LCS matching.

- [ ] **Step 5: Run and commit the checkpoint tests**

```bash
cd backend
go test ./internal/cli ./internal/adapters/chatdriver/cursoracp -count=1
git add internal/cli/hooks.go internal/cli/hooks_test.go internal/adapters/chatdriver/cursoracp/driver.go internal/adapters/chatdriver/cursoracp/checkpoint.go internal/adapters/chatdriver/cursoracp/checkpoint_test.go
git commit -m "feat: verify Cursor native handoff checkpoints"
```

Expected: PASS.

### Task 4: Prove ACP History Convergence for Cursor

**Files:**
- Modify: `backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go`
- Modify: `backend/internal/adapters/chatdriver/acp/driver_test.go`
- Modify: `backend/internal/service/chat/checkpoint_recovery_test.go`

**Interfaces:**
- Consumes: existing ACP `ChatHistoryRefresher` and Chat service `RequireNativeHistory` reconciliation.
- Produces: regression coverage showing Cursor needs no provider-specific transcript augmentation.

- [ ] **Step 1: Add failing generic ACP replay cases matching Cursor's proven stream**

Add an ACP fixture that replays the exact Task 1 event ordering and identities. Assert `ReadHistory` returns one `TurnStarted`, one `UserMessageCompleted`, assistant events, and one recovered `TurnCompleted` for each native turn, with stable `ProviderEventID` values across `RefreshHistory`.

- [ ] **Step 2: Add failing Chat service checkpoint-convergence cases**

Add Cursor-labelled cases proving a verified native boundary and AO high-water mark converge, a missing TUI turn remains `ErrChatHistoryUnsettled`, repeated text with different ids is not conflated, and refresh eventually succeeds only after the missing stable identity appears.

- [ ] **Step 3: Run the focused tests**

```bash
cd backend
go test ./internal/adapters/chatdriver/acp ./internal/service/chat -run 'History|Checkpoint' -count=1
```

Expected: existing generic code should pass most cases. If a failure reveals only Cursor wire normalization, fix it in the Cursor driver wrapper. Change generic ACP history code only when the fixture demonstrates a protocol-generic defect and all existing ACP tests remain green.

- [ ] **Step 4: Rerun the real conformance test**

```bash
cd backend
AO_LIVE_CURSOR_HANDOFF=1 go test ./internal/adapters/chatdriver/cursoracp -run '^TestLiveCursorInterfaceHandoff$' -count=1 -v
```

Expected: PASS without reading provider transcripts into the Chat projection.

- [ ] **Step 5: Commit history coverage**

```bash
git add backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go backend/internal/adapters/chatdriver/acp/driver_test.go backend/internal/service/chat/checkpoint_recovery_test.go
git commit -m "test: enforce Cursor handoff history convergence"
```

### Task 5: Enable Cursor Through the Existing Session Manager Saga

**Files:**
- Modify: `backend/internal/session_manager/interface_transition_test.go`
- Modify only if a generic defect is exposed: `backend/internal/session_manager/interface_transition.go`

**Interfaces:**
- Consumes: Cursor's `AgentInterfaceHandoff`, history probe, checkpoint verifier, and ACP `ChatHistoryRefresher` from Tasks 2–4.
- Produces: Cursor visibility and execution through the existing interface-transition API.

- [ ] **Step 1: Add Cursor capability-status and happy-path tests**

Register the real Cursor adapter in the transition test registry with fake runtime/Chat boundaries. Assert `InterfaceTransitionStatus` exposes switching only when the current TUI launch owns the same native id, TUI → Chat stops before ACP starts and requires history replay, and Chat → TUI closes ACP before invoking `GetRestoreCommand` with the same id.

- [ ] **Step 2: Add fail-closed and recovery tests**

Cover missing current-launch identity proof, absent native history, checkpoint mismatch, ACP replay timeout, resumed TUI id mismatch, source-stop uncertainty, target-start failure rollback, stale hook generation, daemon restart, repeated request, and queued transition-message delivery. Assert no case leaves two Cursor controllers live.

- [ ] **Step 3: Run the Session Manager tests and make only surgical generic fixes**

```bash
cd backend
go test ./internal/session_manager -run 'InterfaceTransition.*Cursor' -count=1
```

Expected: PASS through existing generic code once the adapter declares its capabilities. If production code must change, add the failing provider-neutral regression first and keep the change inside the current coordinator boundary.

- [ ] **Step 4: Run all handoff regressions**

```bash
cd backend
go test ./internal/session_manager ./internal/service/chat ./internal/adapters/agent/claudecode ./internal/adapters/agent/codex ./internal/adapters/agent/cursor ./internal/adapters/chatdriver/claudeacp ./internal/adapters/chatdriver/cursoracp -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit coordinator enablement**

```bash
git add backend/internal/session_manager/interface_transition_test.go backend/internal/session_manager/interface_transition.go
git commit -m "feat: enable Cursor local interface handoff"
```

Omit `interface_transition.go` from `git add` when the existing coordinator needs no production change.

### Task 6: Document and Fully Verify the Feature

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/STATUS.md`

**Interfaces:**
- Consumes: passing evidence from Tasks 1–5.
- Produces: accurate support documentation and final verification evidence.

- [ ] **Step 1: Update support documentation**

List Cursor beside Claude Code and Codex only after the live and automated gates pass. State the exact minimum verified Cursor version and platform, the shared `CURSOR_DATA_DIR` requirement, and any untested OS as a validation gap.

- [ ] **Step 2: Run formatting and focused verification**

```bash
gofmt -w backend/internal/adapters/agent/cursor/continuation.go backend/internal/adapters/agent/cursor/cursor.go backend/internal/adapters/agent/cursor/cursor_test.go backend/internal/adapters/chatdriver/cursoracp/checkpoint.go backend/internal/adapters/chatdriver/cursoracp/checkpoint_test.go backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go backend/internal/adapters/chatdriver/cursoracp/driver.go backend/internal/cli/hooks.go backend/internal/cli/hooks_test.go backend/internal/session_manager/interface_transition_test.go
git diff --check
cd backend && go test ./internal/adapters/agent/cursor ./internal/adapters/chatdriver/cursoracp ./internal/adapters/chatdriver/acp ./internal/service/chat ./internal/session_manager ./internal/cli
```

Expected: PASS.

- [ ] **Step 3: Run repository validation**

```bash
npm run lint
npm run frontend:typecheck
cd backend && go build ./... && go test ./... && go test -race ./... && go vet ./...
cd ../frontend && npm run build
```

Expected: PASS. Report any platform, Docker, credential, or time-bound validation gap exactly; do not claim it passed locally.

- [ ] **Step 4: Run the final live provider gate**

```bash
cd backend
AO_LIVE_CURSOR_HANDOFF=1 go test ./internal/adapters/chatdriver/cursoracp -run '^TestLiveCursorInterfaceHandoff$' -count=1 -v
```

Expected: PASS on the documented Cursor version.

- [ ] **Step 5: Commit documentation and final mechanical changes**

```bash
git add docs/architecture.md docs/STATUS.md
git commit -m "docs: mark Cursor interface handoff supported"
```

- [ ] **Step 6: Review the branch without touching unrelated changes**

```bash
git status --short
git log --oneline --decorate -8
git diff main...HEAD --stat
git diff --check main...HEAD
```

Expected: only the design, plan, Cursor handoff implementation/tests, and corresponding documentation appear in the implementation worktree.
