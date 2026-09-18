# Task 3 production report

Status: **BLOCKED** — safe bidirectional Cursor handoff cannot be implemented from the available provider contract. The required fail-closed fallback and hook identity fix are implemented and verified.

## Commits

- Base before this task: `fcf42f8fc328a0bf0e69893b13f5a7a40e95294a`.
- Production fix and regression tests: `4de5d94ab6a40b721d1549bdc23cf3dc7b29fc60` (`fix: gate Cursor handoff and protect native hook identity`).
- This report is committed separately after the production commit.

## Why production handoff remains blocked

The current `ports.NativeCheckpointVerifier` contract requires an exact native user-message identity, with provider-backed ancestry and completion evidence, which must agree with a completed replay turn. AO high-water and controller ownership checks remain additional requirements.

The available Cursor 2026.09.02-c22c1a3 static bundle and the PR #4416 reference do not supply that cross-surface identity binding:

- `beforeSubmitPrompt` and `stop` contain `conversation_id` and `generation_id`; the latter identifies the hook generation, not an established ACP user-message identity.
- The installed bundle's `agent-transcript` serializer writes citation JSONL shaped as `{role, message: {content}}`. It omits native user/message and generation IDs. Tool-use citation blocks also omit native tool-call IDs.
- The bundle can append a `turn_ended` record containing a terminal status. This is real completion status, but the marker still has no generation/message identity with which to bind a specific hook observation or ACP replay boundary. File position or matching text would have to invent the missing relationship.
- PR #4416's `handoff_history.go` aligns user/assistant text with LCS, derives imported identities from ordinal position, skips malformed JSON records, and marks a turn completed when assistant text is nonempty. It does not prove the current checkpoint contract. Repeated prompts or partial transcripts make those assumptions unsafe.
- The reference's transport-envelope reconstruction does not establish native message identity, and cannot discharge this gap by hiding duplicated history from the AO projection.

No identity/completion verifier or transcript bridge was invented to bypass these requirements. Further work needs a provider-backed stable native identity and completed-boundary representation, including a proven mapping to ACP replay. Reverse engineering a different internal blob store would be a new unverified provider contract, not evidence that the supplied transcript bridge satisfies the current contract.

## Behavior implemented

- Removed `Cursor.Plugin.NativeConversationID` and its `AgentInterfaceHandoff` assertion. Cursor therefore no longer advertises interface switching.
- Real Session Manager regression tests cover both TUI-to-Chat and Chat-to-TUI. Status returns `INTERFACE_HANDOFF_UNSUPPORTED`, and transition admission returns `ErrInterfaceHandoffUnsupported` before creating a transition or touching controllers.
- Kept the existing AO-owned transcript-location probe and its safety tests as preparatory code. It does not expose the handoff capability, read transcript contents, project history, or bypass admission.
- Added one hook resume-identity dispatcher covering worker hooks, reviewer hooks, and Cursor permission hooks. Cursor identity is accepted only from `session-start`, using native `conversation_id`, a matching startup `generation_id`, and explicit `is_background_agent: false`.
- Rejected child/parent identity markers and foreign session/conversation aliases, malformed JSON, missing foreground proof, mismatched generation, overlong IDs, and IDs outside the existing safe ID alphabet.
- Stop, submission, tool, and permission callbacks cannot overwrite Cursor's durable native resume ID. Main foreground startup can still report it, with the existing independent AO launch fence.
- Cursor conversation facts remain disabled. No submission context, synthetic prompt, history migration, terminal transcript rendering, or cloud runner was added.
- Shared ACP, Chat service, generation fencing, rollback/restart recovery, message outbox, and controller ownership production code remain unchanged.

## Files changed

- `backend/internal/adapters/agent/cursor/continuation.go`: remove the handoff identity method and document the missing proof.
- `backend/internal/adapters/agent/cursor/cursor.go`: remove the handoff capability assertion.
- `backend/internal/adapters/agent/cursor/cursor_test.go`: remove tests of the withdrawn capability; retain owned transcript probing and ordinary launch/restore tests.
- `backend/internal/cli/hooks.go`: constrain Cursor resume identity at every hook dispatch path.
- `backend/internal/cli/hooks_test.go`: reproduce and prevent the subagent/foreign/non-start overwrite; test worker and reviewer routes and permission paths; strengthen the existing unverified-facts test.
- `backend/internal/session_manager/interface_transition_test.go`: prove both transition directions reject before controller mutation using the real Cursor adapter.
- `.superpowers/sdd/2026-09-18-cursor-local-interface-handoff/task-3-production-report.md`: this report.

## Verification

Environment: `go version go1.27.0 darwin/arm64`. Commands below ran from `/tmp/ao-cursor-local-handoff/backend` unless noted.

Test-first reproductions, before production edits:

- `go test ./internal/cli -run 'TestHooks_Cursor(ResumeIdentity|DoesNotReport)' -count=1` — FAILED as expected. Worker/reviewer stop, submission, tool, permission, child/background/foreign startup payloads reported unwanted `AgentSessionID`, including the original `cursor-child` reproduction.
- `go test ./internal/session_manager -run TestCursorInterfaceTransitionRejectsUnverifiedHistoryBeforeTouchingControllers -count=1` — FAILED as expected. Cursor still exposed handoff support and reached native-session probing instead of rejecting the capability.

After production edits:

- `go test ./internal/cli -run 'TestHooks_Cursor(ResumeIdentity|DoesNotReport)' -count=1` — PASS (1.731s).
- `go test ./internal/session_manager -run TestCursorInterfaceTransitionRejectsUnverifiedHistoryBeforeTouchingControllers -count=1` — PASS (1.013s).
- `go test ./internal/adapters/agent/cursor ./internal/adapters/chatdriver/cursoracp ./internal/adapters/chatdriver/acp ./internal/cli ./internal/service/chat ./internal/session_manager` — PASS for all six complete package suites: Cursor 1.763s, Cursor ACP 1.476s, ACP 6.716s, CLI 12.362s, Chat service 65.693s, Session Manager 57.527s.
- `go vet ./internal/adapters/agent/cursor ./internal/cli ./internal/session_manager` — PASS (exit 0, no output).
- `git diff --check` (worktree root) — PASS before committing.
- `git diff fcf42f8fc328a0bf0e69893b13f5a7a40e95294a..HEAD --check` (worktree root, production commit) — PASS.

The full repository CI matrix, race suite, frontend checks, and remote CI were not run for this bounded production phase. No PR was created or handed over as CI-validated. The required six package suites, changed-package vet, and whitespace check completed.

## Self-review findings

Reviewed the complete six-file production/test diff from the supplied base and all remaining hook identity call sites.

- The permission hook and reviewer hook were independent identity-write paths; both now use the same guarded dispatcher. A worker-only patch would have left the bug reachable.
- Merely removing the compile-time capability assertion would not disable a Go interface. The exported method was removed too; the Session Manager behavior tests prove actual rejection.
- The location probe remains independent of switching support and cannot satisfy the checkpoint verifier by itself.
- Static inspection found a `turn_ended` status record. The blocker is absence of a stable identity binding, not absence of all completion-status data; comments/report were corrected accordingly.
- Main session-start shape is based on the installed 2026.09.02-c22c1a3 bundle. A different Cursor version that omits the explicit foreground flag, changes the startup generation relationship, or adds foreign identity aliases will be rejected for resume-metadata capture. This conservative compatibility limitation is intentional; no minimum live-verified version is claimed.
- No additional production defect was found in the final diff. Existing generic lifecycle semantics were preserved, and the required package suites passed.

## Explicit live-validation gap

Live provider validation is **pending and was deliberately not attempted in this task**, per the user's direction. No Cursor authentication, account import, authenticated turn, or real interactive handoff was requested or performed. The opt-in live tests were not enabled during the ordinary package suites.

The earlier harness records an isolated-profile authentication block before completing the provider contract. No successful ACP-to-TUI-to-ACP-to-TUI sequence, same-ID assertion, stable ACP/native history mapping, or bounded flush convergence is claimed here. Cursor switching remains disabled until those provider-backed contracts can be safely established.
