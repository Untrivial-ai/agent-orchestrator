# Task 3 production report

Status: **BLOCKED** — safe bidirectional Cursor handoff cannot be implemented from the available provider contract. The required fail-closed fallback and hook identity fix are implemented and verified.

## Commits

- Base before this task: `fcf42f8fc328a0bf0e69893b13f5a7a40e95294a`.
- Production fix and regression tests: `4de5d94ab6a40b721d1549bdc23cf3dc7b29fc60` (`fix: gate Cursor handoff and protect native hook identity`).
- Initial report: `fb4cf96e3069ac34b99ecdbb8750b65c6ec5b521` (`docs: record Cursor handoff safety fallback and validation gap`).
- Final review corrections are recorded below and committed with this report update.

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
- Removed the citation transcript-location probe and its `AgentInterfaceHandoffHistoryProbe` assertion after final review established that ordinary restore also consumes that interface. Missing citation JSONL cannot prove the native conversation is lost. Cursor now implements neither interface, and ordinary restore preserves the native `--resume` command independently of citation state.
- Added one hook resume-identity dispatcher covering worker hooks, reviewer hooks, and Cursor permission hooks. Cursor identity is accepted only from `session-start`, using native `conversation_id`, a matching startup `generation_id`, and explicit `is_background_agent: false`.
- Rejected child/parent identity markers and foreign session/conversation aliases, malformed JSON, missing foreground proof, mismatched generation, overlong IDs, and IDs outside the existing safe ID alphabet.
- Stop, submission, tool, and permission callbacks cannot overwrite Cursor's durable native resume ID. Main foreground startup can still report it, with the existing independent AO launch fence.
- Cursor conversation facts remain disabled. No submission context, synthetic prompt, history migration, terminal transcript rendering, or cloud runner was added.
- Shared ACP, Chat service, generation fencing, rollback/restart recovery, message outbox, and controller ownership production code remain unchanged.

## Files changed

- `backend/internal/adapters/agent/cursor/continuation.go`: deleted after withdrawing both the handoff identity method and the unproven citation probe.
- `backend/internal/adapters/agent/cursor/cursor.go`: remove both handoff capability and history-probe assertions.
- `backend/internal/adapters/agent/cursor/cursor_test.go`: remove tests of the withdrawn capability/probe; retain ordinary launch/restore tests.
- `backend/internal/cli/hooks.go`: constrain Cursor resume identity at every hook dispatch path.
- `backend/internal/cli/hooks_test.go`: reproduce and prevent the subagent/foreign/non-start overwrite; test worker and reviewer routes and permission paths; strengthen the existing unverified-facts test.
- `backend/internal/session_manager/interface_transition_test.go`: prove both transition directions reject before controller mutation using the real Cursor adapter.
- `backend/internal/session_manager/cursor_restore_test.go`: prove real Cursor command construction through the Session Manager preserves `--resume` with missing, empty, and unrecognized citation state.
- `backend/internal/adapters/chatdriver/cursoracp/handoff_live_test.go`: fix expected-marker chronology and add ordered, reversed, missing, and duplicate fixture coverage.
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
- Final review caught an additional consumer of the retained location probe: ordinary restore treats a false result as proof of lost native state. The probe and its tests are now removed; real Cursor restore regression coverage proves that citation absence cannot trigger a fresh saved-prompt launch.
- Static inspection found a `turn_ended` status record. The blocker is absence of a stable identity binding, not absence of all completion-status data; comments/report were corrected accordingly.
- Main session-start shape is based on the installed 2026.09.02-c22c1a3 bundle. A different Cursor version that omits the explicit foreground flag, changes the startup generation relationship, or adds foreign identity aliases will be rejected for resume-metadata capture. This conservative compatibility limitation is intentional; no minimum live-verified version is claimed.
- The initial self-review missed that ordinary-restore consumer; it is corrected in the final fix wave below. Existing generic lifecycle production code remains unchanged.

## Explicit live-validation gap

Live provider validation is **pending and was deliberately not attempted in this task**, per the user's direction. No Cursor authentication, account import, authenticated turn, or real interactive handoff was requested or performed. The opt-in live tests were not enabled during the ordinary package suites.

The earlier harness records an isolated-profile authentication block before completing the provider contract. No successful ACP-to-TUI-to-ACP-to-TUI sequence, same-ID assertion, stable ACP/native history mapping, or bounded flush convergence is claimed here. Cursor switching remains disabled until those provider-backed contracts can be safely established.

## Final fix wave after whole-branch review

Final-fix status: **DONE_WITH_CONCERNS**. The requested final corrections are complete; the overall handoff feature remains BLOCKED for the provider-contract reasons above.

The review identified an Important restore regression: `manager.nativeConversationMissing` consumes `AgentInterfaceHandoffHistoryProbe` even without the handoff capability. The retained Cursor citation probe returned false for missing or empty citation state, causing `restoreArgv` to replace a valid `--resume` command with a saved-prompt fresh launch. Removed `continuation.go` entirely, removed the history-probe assertion, and removed tests of that withdrawn probe. Existing ordinary Cursor launch/restore tests remain. A new regression exercises the real Cursor adapter through `restoreArgv`, using a non-executed temporary binary and missing/empty/unrecognized citation layouts; all preserve the exact native resume argv and native restore mode.

The Minor chronology finding was also reproduced: appending traversal event indices produces an inherently sorted list even when expected markers appear in reverse order. `cursorHandoffMarkerCounts` now appends the expected marker index as each marker is encountered. Fixture tests cover correct order, reversed order, missing markers, duplicates, and ignoring assistant mentions. No live provider invocation is needed for those tests.

Exact final-fix evidence (commands run from `backend/`):

- Before fixes, `go test ./internal/session_manager -run TestRestoreArgvCursorPreservesResumeWithoutCitationTranscript -count=1` — FAILED as expected in all three citation-state cases: returned saved-prompt argv instead of `--resume cursor-native-1`.
- Before fixes, `go test ./internal/adapters/chatdriver/cursoracp -run TestCursorHandoffMarkerCountsRejectsReversedChronology -count=1` — FAILED as expected for reversed history: counts `[1 1]`, order `[1 2]`, incorrectly converged.
- After fixes, `go test ./internal/session_manager -run 'TestRestoreArgvCursorPreservesResumeWithoutCitationTranscript|TestCursorInterfaceTransitionRejectsUnverifiedHistoryBeforeTouchingControllers' -count=1` — PASS (1.093s).
- After fixes, `go test ./internal/adapters/chatdriver/cursoracp -run TestCursorHandoffMarkerCountsRejectsReversedChronology -count=1` — PASS (0.807s).
- `go test ./internal/adapters/agent/cursor ./internal/adapters/chatdriver/cursoracp ./internal/session_manager` — PASS for all three complete affected package suites (1.145s, 0.522s, 44.446s respectively).
- `go vet ./internal/adapters/agent/cursor ./internal/adapters/chatdriver/cursoracp ./internal/session_manager` — initial sandbox run failed to access Go build-cache entries and consequently reported unresolved standard packages; the identical command rerun with approved build-cache access PASSED (exit 0, no output).
- `git diff --check` at the worktree root — PASS.

Final review checked the complete fix diff, the removed probe's call sites, the actual restore argv result, and both chronology-check consumers. No handoff identity method or history-probe interface remains on Cursor. Generic Session Manager production code is unchanged, and the report's earlier probe-retention claims have been corrected.

Deferred Minor finding: the pre-existing opt-in `TestLiveCursorACP` in `live_test.go` calls `driver.Probe` before resolving the AO data root and can therefore consult ambient auth. It is unchanged in this bounded fallback fix. The live tests remain disabled during automated package verification; no authentication, account import, or live handoff was attempted. This preflight needs an explicit owned-profile check before future live validation, and is not evidence of a verified provider contract.
