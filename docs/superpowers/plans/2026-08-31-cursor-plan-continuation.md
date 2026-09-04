# Cursor Plan Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reproduce and fix Cursor ACP sessions that remain idle after AO accepts a generated plan, without weakening AO's permission boundaries.

**Architecture:** Exercise the real Cursor ACP adapter in provider Plan mode while launching Cursor with AO's existing auto-review permission mode. The test accepts only the custom `cursor/create_plan` approval and then requires the same provider turn to create a proof file, which separates plan continuation from ordinary tool approval behavior. Use the resulting ACP event sequence to make the smallest Cursor-specific adapter correction, leaving the generic ACP transport and durable approval controller unchanged unless the trace proves they are responsible.

**Tech Stack:** Go, Cursor Agent ACP, `coder/acp-go-sdk`, AO chat-driver ports, Go tests.

---

### Task 1: Add the account-backed plan continuation regression

**Files:**
- Modify: `backend/internal/adapters/chatdriver/cursoracp/live_test.go`

- [x] **Step 1: Add a live test that selects Cursor Plan mode**

Add `TestLiveCursorACPPlanAcceptanceContinues` beside the permission-mode matrix. Start the conversation with `PermissionModeAuto`, find the advertised `mode` option, require a `plan` choice, and select it through `ChatConfigOptionController.SetConfigOption`.

```go
func TestLiveCursorACPPlanAcceptanceContinues(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_ACP") != "1" {
		t.Skip("set AO_LIVE_CURSOR_ACP=1 to run against the local Cursor account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	conv, err := New(cursor.New(), nil).Start(ctx, ports.ChatStartConfig{
		SessionID: "live-cursor-plan-continuation", DataDir: liveDataDir(t),
		WorkspacePath: workspace, Env: liveEnvMap(), Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	setCursorPlanMode(ctx, t, conv)
	ref := sendLiveTurnWithSettings(ctx, t, conv,
		"Create a plan first. After I approve it, use the file editing tool to create plan-proof.txt containing continued, then say done.",
		ports.ChatTurnSettings{Approval: ports.PermissionModeAuto})
	waitForApprovedCursorPlan(ctx, t, conv, ref.ProviderTurnID)
	content, err := os.ReadFile(filepath.Join(workspace, "plan-proof.txt"))
	if err != nil || strings.TrimSpace(string(content)) != "continued" {
		t.Fatalf("plan continuation proof = %q, %v", content, err)
	}
}
```

- [x] **Step 2: Add focused helpers that reject a second approval**

Add `setCursorPlanMode` and `waitForApprovedCursorPlan`. The waiter must resolve only an `ActivityKindPlan` approval, fail on any later approval, and require the original turn to complete successfully.

```go
func setCursorPlanMode(ctx context.Context, t *testing.T, conv ports.ChatConversation) {
	t.Helper()
	controller := conv.(ports.ChatConfigOptionController)
	options, err := controller.ListConfigOptions(ctx)
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	for _, option := range options {
		if option.ID != "mode" {
			continue
		}
		for _, choice := range option.Choices {
			if choice.Value == "plan" {
				if _, err := controller.SetConfigOption(ctx, "mode", ports.ChatConfigOptionValue{Select: choice.Value}); err != nil {
					t.Fatalf("SetConfigOption(mode=plan): %v", err)
				}
				return
			}
		}
	}
	t.Fatalf("Cursor did not advertise mode=plan: %#v", options)
}

func waitForApprovedCursorPlan(ctx context.Context, t *testing.T, conv ports.ChatConversation, turnID string) {
	t.Helper()
	approved := false
	for {
		select {
		case event, ok := <-conv.Events():
			if !ok {
				t.Fatal("controller closed before plan continuation completed")
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != turnID {
				continue
			}
			switch event.Kind {
			case ports.ChatEventApprovalRequested:
				if approved || event.ActivityKind != domain.ActivityKindPlan {
					t.Fatalf("unexpected approval after accepting Cursor plan: %#v", event)
				}
				if err := conv.ResolveRequest(ctx, event.RequestID, ports.ChatDecision{ID: "accept"}); err != nil {
					t.Fatalf("ResolveRequest(plan): %v", err)
				}
				approved = true
			case ports.ChatEventTurnCompleted:
				if !approved || event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("plan turn completed without continuation: approved=%v state=%q", approved, event.TurnState)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("Cursor did not continue after plan approval: %v", ctx.Err())
		}
	}
}
```

- [x] **Step 3: Verify ordinary tests remain hermetic**

Run:

```bash
cd backend
go test ./internal/adapters/chatdriver/cursoracp -count=1
```

Expected: PASS with the new test skipped unless `AO_LIVE_CURSOR_ACP=1` is set.

- [x] **Step 4: Run the live regression against the authenticated issue-era Cursor CLI**

Run:

```bash
cd backend
AO_LIVE_CURSOR_ACP=1 go test ./internal/adapters/chatdriver/cursoracp \
  -run TestLiveCursorACPPlanAcceptanceContinues -count=1 -v -timeout 6m
```

Expected before the fix: FAIL after the plan is accepted, with either a second approval, an incomplete turn, or a timeout. Preserve the exact observed event sequence in the implementation notes.

Observed with Cursor Agent `2026.08.25-3e8eec8`: AO received and accepted the plan, Cursor completed the original turn successfully, but `plan-proof.txt` was absent. No second approval occurred. Cursor remained in Plan mode, so accepting `cursor/create_plan` did not perform Cursor's separate Build transition into Agent mode.

- [x] **Step 5: Commit the isolated reproducer**

```bash
git add backend/internal/adapters/chatdriver/cursoracp/live_test.go \
  docs/superpowers/plans/2026-08-31-cursor-plan-continuation.md
git commit -m "test: reproduce cursor plan continuation stall"
```

### Task 2: Continue the same AO turn in Cursor Agent mode

**Files:**
- Modify: `backend/internal/adapters/chatdriver/acp/extensions.go`
- Modify: `backend/internal/adapters/chatdriver/acp/conversation.go`
- Modify: `backend/internal/adapters/chatdriver/acp/driver_test.go`
- Modify: `backend/internal/adapters/chatdriver/cursoracp/extensions.go`
- Modify: `backend/internal/adapters/chatdriver/cursoracp/driver_test.go`

- [x] **Step 1: Classify the live failure from observable evidence**

Use this decision table; do not modify more than the row selected by the trace.

| Observation after `accepted` | Boundary | Required correction |
| --- | --- | --- |
| Cursor emits tool/message events but AO still reports pending | generic ACP conversation state | clear only the resolved extension request and retain the active provider turn |
| Cursor immediately emits another plan approval | Cursor extension adapter | de-duplicate the same provider plan tool call while preserving new plan revisions |
| Cursor emits an ordinary tool permission | no transport defect | preserve the permission request; improve only test/diagnostic wording because plan consent is not tool consent |
| Cursor emits nothing until timeout | Cursor provider behavior | do not auto-grant or synthesize a detached turn; retain the regression and document the provider incompatibility with the captured Cursor version |

Selected observation: Cursor emitted a successful turn completion immediately after the accepted plan but performed no implementation. Applying the advertised `mode=agent` session option changed future turns but did not resume the already-running Plan prompt. The correction therefore has two bounded pieces: the Cursor extension adapter performs the explicit Build transition and requests continuation, while the generic ACP loop executes that one queued prompt before completing the same durable AO turn.

- [x] **Step 2: Write a deterministic failing test at the selected boundary**

Extend `fakeExtensionBridge` with config and continuation methods. Update `TestHandleCreatePlanPublishesPlanAndUsesDurableApprovalFlow` to require `mode=agent` plus the continuation instruction, cover rejected/cancelled plans, and require mode/continuation failures to surface. Add a generic ACP transport test that requires two sequential provider prompts but only one AO turn-start/turn-complete pair.

Run the single new test with `-count=1`; expected result is FAIL before implementation.

- [x] **Step 3: Implement the minimal correction**

Add `SetConfigOption` and `RequestTurnContinuation` to `ClientExtensionBridge`. After `handleCreatePlan` receives `accept`, call:

```go
if _, err := bridge.SetConfigOption(ctx, "mode", ports.ChatConfigOptionValue{Select: "agent"}); err != nil {
	return nil, fmt.Errorf("switch Cursor to agent mode after plan approval: %w", err)
}
if err := bridge.RequestTurnContinuation(ctx, "Implement the approved plan now."); err != nil {
	return nil, fmt.Errorf("continue Cursor turn after plan approval: %w", err)
}
```

The generic ACP `runTurn` loop consumes a queued continuation only after the current prompt ends successfully, assigns it a fresh provider message ID, and emits no intermediate AO turn completion. Then the Cursor handler returns the existing accepted response. Rejected and cancelled outcomes must return without changing mode or scheduling work. The implementation must keep these invariants:

```text
plan approval accepts only cursor/create_plan
tool permissions still require their configured AO policy
rejected and cancelled plans never continue
the existing provider turn remains the durable owner of all continuation events
```

- [x] **Step 4: Verify the deterministic and live regressions**

Run:

```bash
cd backend
go test ./internal/adapters/chatdriver/acp ./internal/adapters/chatdriver/cursoracp -count=1
AO_LIVE_CURSOR_ACP=1 go test ./internal/adapters/chatdriver/cursoracp \
  -run TestLiveCursorACPPlanAcceptanceContinues -count=1 -v -timeout 6m
go test -race ./internal/adapters/chatdriver/acp ./internal/adapters/chatdriver/cursoracp
```

Observed: focused ACP/Cursor tests passed, and the authenticated live test passed in 41.45 seconds with `plan-proof.txt` created during the same AO turn. The race command remains part of final verification.

- [x] **Step 5: Commit the minimal fix**

```bash
git add backend/internal/adapters/chatdriver/acp backend/internal/adapters/chatdriver/cursoracp backend/internal/service/chat
git commit -m "fix: continue cursor work after plan approval"
```

### Task 3: Repository verification and local handoff

**Files:**
- Modify: `docs/superpowers/plans/2026-08-31-cursor-plan-continuation.md` to mark completed checkboxes and record the observed root cause.

- [x] **Step 1: Run repository checks**

```bash
npm run lint
npm run frontend:typecheck
```

Expected: PASS, except the already-observed environment-dependent Codex protocol conformance mismatch must be reported separately if it recurs.

Observed:

- `npm run frontend:typecheck`: PASS after installing the fresh worktree's frontend and shared product-UI dependencies.
- pinned `golangci-lint` v2.12.2: PASS with `0 issues`.
- `npm run lint`: all changed Cursor/ACP packages passed, but the aggregate Go test step reported the pre-existing installed-Codex/schema mismatch plus two environment/load failures. The timing-sensitive CLI test passed immediately on retry; the project telemetry test passed with the machine's global GPG signing config disabled.
- a CI-like `go test ./...` run with the incompatible local Codex binary hidden passed every package except `session_manager`, where the same reduced PATH also hid `tmux`; that package passed separately with `tmux` restored.
- final authenticated Cursor live regression: PASS in 28.11 seconds.

- [x] **Step 2: Confirm the branch contains no unrelated changes**

```bash
git status --short
git diff --stat origin/main...HEAD
git diff --check origin/main...HEAD
```

Expected: only the Cursor plan regression, its minimal proven fix, and this plan document; no whitespace errors.

- [x] **Step 3: Leave the work local**

Do not push and do not create a pull request. Report the branch name, worktree path, reproduction evidence, root cause, changed files, commits, and exact verification results.
