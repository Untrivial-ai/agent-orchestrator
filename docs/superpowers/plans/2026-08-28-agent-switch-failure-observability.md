# Agent Switch Failure-Only Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build end-to-end, consent-aware, failure-only Sentry observability for asynchronous TUI and Chat agent switching, from durable classification through recovery and frontend visibility, while successful switches emit nothing.

**Architecture:** The Session Manager classifies semantic failures and couples every winning failure or first-marker CAS to a dedicated SQLite receipt/outbox transaction. A daemon-owned policy coordinator, delivery gate, dispatcher, and synchronous Sentry envelope sender deliver immutable privacy-allowlisted events; Electron main is the sole desktop sender and owns visibility-only incidents. Correctness prerequisites land first, every phase is a separate reviewable commit on `codex/agent-switch-failure-observability`, and production capture remains disabled until the documented privacy and runbook release gates are approved.

**Tech Stack:** Go 1.25.7, SQLite/goose/sqlc, chi HTTP, sentry-go only for existing generic capture, dedicated `net/http` Sentry envelope transport, Electron 33, React 19, TypeScript 5.6, Vitest, Go `testing`/`httptest`.

**Spec:** `docs/superpowers/specs/2026-08-28-agent-switch-failure-observability-design.md`

## Global Constraints

- Successful switches, expected validation/conflicts, idempotent replay, stale callbacks, successful fallback/read-back recovery, and successful cleanup create zero receipts, zero outbox rows, and zero Sentry events.
- The switch/session rows remain the only ownership authority; reporting can never change ownership, compensation, recovery gates, or terminal settlement.
- `source_stop_unconfirmed`, `source_restore_unconfirmed`, and `target_start_unconfirmed` are nonterminal retained markers and are forbidden on `failed` rows; `delivery_unconfirmed` is terminal.
- No Sentry network call occurs while the SQLite writer lock is held, and no direct semantic Sentry fallback is allowed after a failed or ambiguous SQLite write.
- Remote delivery is at-least-once with one stable 32-character lowercase hexadecimal EventID; local enrollment is exactly once per retained dedupe receipt when the telemetry savepoint succeeds.
- Canonical event JSON is at most 60 KiB, its deterministic envelope wrapper is at most 4 KiB, the complete envelope is at most 64 KiB, and sanitized stacks are at most 16 KiB.
- Outbox payload TTL is exactly seven days; claims lease for 30 seconds; each provider call is bounded to five seconds; the authority watcher polls every one second.
- Retry bases are 5 seconds, 30 seconds, 5 minutes, 30 minutes, then 6 hours for the fifth and subsequent attempts, each with equal jitter in the inclusive 50-100 percent range.
- Visibility grace periods are exactly 15 seconds for active/recovery state, 60 seconds for history, 2 seconds for presentation acknowledgement, and 5 continuous healthy minutes before recurrence.
- Remote application fields are deny-by-default. Never export switch/session/project/workspace/user IDs, paths, prompts, messages, transcript or terminal content, provider/native/generation/runtime identifiers, argv/environment, URLs, branches/issues/PR facts, hashes, idempotency keys, request fingerprints, or raw errors/panic values.
- `AO_DATA_DIR/telemetry_policy.json` is the desktop consent authority. Electron main is its sole durable generation writer; daemon mirrors never invent desktop generations; malformed, unsafe, unreadable, or unsupported policy fails closed.
- The primary listener remains unauthenticated loopback-only. Policy control routes live under `/internal/agent-switch-observability/*`, require the existing local-control guard, and remain blocked by the LAN listener's `/internal/*` rule.
- Existing migrations are immutable, `change_log` remains trigger-owned, generated sqlc files are regenerated rather than hand-edited, and OpenAPI plus `frontend/src/api/schema.ts` are regenerated together.
- The specification prefers several PRs, but the user's explicit integration instruction overrides that packaging preference: use one branch and one PR, preserve phases as the separate commits below, and keep the production release gate closed.
- The PR must not flip `agentSwitchFailureProductionEnabled` to true. Nonproduction tests inject an enabled release gate; production remains disabled until `docs/telemetry.md`, the runbook, and Sentry IP storage/scrubbing, retention, residency, and automatic-context settings have all been approved.
- Windows remains fail-closed for desktop event consent in this PR unless a tested replace/write-through native primitive lands; ordinary Node rename is not accepted as proof of the design's Windows crash-durability contract, and this limitation is a documented release blocker rather than a weakened guarantee.

## File Structure and Ownership

**Create:**

- `backend/internal/domain/agent_switch_observability.go` — stable taxonomy, typed fault/event/policy/outbox values, applicability validation, dedupe and fingerprint construction.
- `backend/internal/domain/agent_switch_observability_test.go` — exhaustive taxonomy, applicability, dedupe, stack, and privacy tests.
- `backend/internal/observe/sentryobs/agent_switch_event.go` — provider-owned canonical Sentry serialization and bounded envelope encoding.
- `backend/internal/ports/agent_switch_observability.go` — observer, reporting-policy, delivery-gate, and outbox store contracts.
- `backend/internal/storage/sqlite/migrations/0125_agent_switch_failure_observability.sql` — `failure_point`, policy, receipt, outbox, and delivery-state schema.
- `backend/internal/storage/sqlite/queries/agent_switch_failure_observability.sql` — policy synchronization, enrollment, claim, attempt, acknowledgement, retry, quarantine, purge, receipt-resolution, and diagnostics queries.
- `backend/internal/storage/sqlite/store/agent_switch_failure_store.go` — atomic CAS/enrollment savepoint and delivery persistence implementation.
- `backend/internal/storage/sqlite/store/agent_switch_failure_store_test.go` — atomicity, races, retention, delete-order, consent, TTL, and delivery-state tests.
- `backend/internal/observe/agentswitch/policy.go` — daemon authority reader, mirrored-policy coordinator, generation/epoch gate, watcher, and disable/apply protocol.
- `backend/internal/observe/agentswitch/policy_test.go` — headless truth table, stale generations, watcher, opt-out races, and fail-closed startup tests.
- `backend/internal/observe/agentswitch/dispatcher.go` — one daemon-lifetime outbox claim/send/settle loop.
- `backend/internal/observe/agentswitch/dispatcher_test.go` — lease, retry, throttle, TTL, shutdown, destination rotation, and duplicate-delivery tests.
- `backend/internal/observe/sentryobs/dsn.go` — strict normalized DSN parsing and destination fingerprinting.
- `backend/internal/observe/sentryobs/agent_switch_sender.go` — bounded synchronous status-aware envelope transport.
- `backend/internal/observe/sentryobs/agent_switch_sender_test.go` — DSN, redirect, TLS/proxy, bounds, rate-limit, status, and byte-stability tests.
- `backend/internal/observe/ownership/ownership.go` and `ownership_test.go` — `http`/`agent_switch_saga` owner enum and wrapping-safe `OwnedError`.
- `backend/internal/session_manager/agent_switch_faults.go` — enum-only flight recorder, classification helpers, stack capture, and store call adapters.
- `backend/internal/session_manager/agent_switch_faults_test.go` — classifier and success-zero-event tests.
- `frontend/src/shared/telemetry-policy.ts` and `.test.ts` — pure desktop/headless-neutral policy wire types, parsing, and validation.
- `frontend/src/main/telemetry-policy-file.ts` and `.test.ts` — crash-durable 0600 authority-file materialization and replacement.
- `frontend/src/main/daemon-telemetry-policy-client.ts` and `.test.ts` — typed loopback-only prepare/apply acknowledgement client.
- `frontend/src/main/desktop-telemetry-controller.ts` and `.test.ts` — serialized consent generations, delivery gate, cache purge, and daemon coordination.
- `frontend/src/renderer/stores/telemetry-policy-store.ts` and `.test.ts` — dynamic policy view, live-change status, and typed settings IPC.
- `frontend/src/shared/agent-switch-observability.ts` and `.test.ts` — typed IPC payloads, canonical visibility envelope encoder, and shared validation.
- `frontend/src/main/agent-switch-observability.ts` and `.test.ts` — focus-owned visibility state machine and dedicated no-cache main-process sender.
- `frontend/src/renderer/lib/agent-switch-visibility.ts` and `.test.ts` — renderer signal adapter with no network transport or DSN.
- `frontend/src/renderer/hooks/useAgentSwitchVisibility.ts` and `.test.tsx` — query/transport/presentation signals and layout-effect acknowledgement.
- `test/fixtures/agent-switch-observability/envelope-v1.json` — cross-language frozen envelope fixture.
- `docs/runbooks/agent-switch-failure-points.md` — every taxonomy point's ownership interpretation, logs/tests, recovery, and release-blocker rule.

**Modify:**

- Agent-switch domain/store/manager/lifecycle files, runtime ports and tmux/ConPTY adapters, daemon config/wiring/startup/shutdown, HTTP router/envelope/logger, session service error mapping, controller/OpenAPI sources and generated artifacts, Electron main/preload/bootstrap, renderer API/query/presentation surfaces, `docs/telemetry.md`, and focused tests named in each task.

---

### Task 1: Correct Chat Activation's Source-to-Target Generation CAS

**Files:**

- Modify: `backend/internal/domain/agent_switching.go` (`AgentSwitchChatTargetActivation`)
- Modify: `backend/internal/storage/sqlite/queries/agent_switching.sql` (`ActivateChatSessionAgentSwitchTarget`)
- Modify: `backend/internal/storage/sqlite/store/agent_switching_store.go` (`ActivateChatAgentSwitchTarget`)
- Modify: `backend/internal/session_manager/agent_switching_chat.go` (`ControllerReady`, activation outcome read-back)
- Test: `backend/internal/storage/sqlite/store/agent_switching_store_test.go`
- Test: `backend/internal/service/chat/controller_test.go`
- Test: `backend/internal/session_manager/agent_switching_test.go`
- Regenerate: `backend/internal/storage/sqlite/gen/agent_switching.sql.go`

**Interfaces:**

- Consumes: stopped Chat session with `sessions.controller_generation == ExpectedSourceControllerGeneration` and a pending provider boundary.
- Produces: `AgentSwitchChatTargetActivation{ExpectedSourceControllerGeneration string, ControllerGeneration string}` whose one transaction changes the source generation to the target generation while committing harness ownership, provider conversation head/native identity, conversation branch, and `target_ready`.

- [ ] **Step 1: Write the failing real-store and real-Chat-service integration tests**

```go
func TestActivateChatAgentSwitchTargetMovesSourceGenerationToTarget(t *testing.T) {
	ctx := context.Background()
	st := openAgentSwitchStore(t)
	seedStoppedChatSwitch(t, st, "source-generation", "target-generation")

	changed, err := st.ActivateChatAgentSwitchTarget(ctx, domain.AgentSwitchChatTargetActivation{
		SwitchID: "switch-1", SessionID: "session-1",
		SourceHarness: "claude-code", SourceGenerationID: "source-generation",
		ExpectedSourceControllerGeneration: "source-generation",
		TargetHarness: "codex", TargetNativeSessionRef: "native-target",
		TargetGenerationID: "target-generation", ProviderConversationID: "provider-target",
		ControllerGeneration: "target-generation", ActivatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, changed)
	got, _, err := st.GetSession(ctx, "session-1")
	require.NoError(t, err)
	require.Equal(t, "target-generation", got.Metadata.ControllerGeneration)
}
```

The Chat integration fixture must run Session Manager + Chat Service + SQLite and assert the fake driver does not preclaim `target-generation` before `ControllerReady`.

- [ ] **Step 2: Run the narrow tests and confirm the current target-generation predicate loses the CAS**

Run: `cd backend && go test ./internal/storage/sqlite/store ./internal/service/chat ./internal/session_manager -run 'TestActivateChatAgentSwitchTargetMovesSourceGenerationToTarget|TestAgentSwitchChatControllerReadyUsesSourceGenerationCAS' -count=1`

Expected: FAIL because `ActivateChatSessionAgentSwitchTarget` currently requires the session row to already equal the target generation.

- [ ] **Step 3: Change the activation command and SQL predicate**

```go
type AgentSwitchChatTargetActivation struct {
	SwitchID                          AgentSwitchID
	SessionID                         SessionID
	SourceHarness                     AgentHarness
	SourceGenerationID                AgentGenerationID
	ExpectedSourceControllerGeneration string
	TargetHarness                     AgentHarness
	TargetNativeSessionRef            AgentNativeSessionID
	TargetGenerationID                AgentGenerationID
	ProviderConversationID            string
	ControllerGeneration              string
	ActivatedAt                       time.Time
}
```

```sql
UPDATE sessions SET
    harness = sqlc.arg(target_harness),
    controller_generation = sqlc.arg(target_controller_generation),
    provider_conversation_id = sqlc.arg(provider_conversation_id),
    agent_session_id = sqlc.arg(target_native_session_id),
    activity_state = 'idle', activity_last_at = sqlc.arg(activated_at),
    first_signal_at = NULL, runtime_handle_id = '', runtime_launch_id = '',
    agent_session_id_launch_id = '', native_transcript_path = '',
    updated_at = sqlc.arg(activated_at)
WHERE id = sqlc.arg(session_id)
  AND is_terminated = 0 AND session_mode = 'chat' AND activity_state = 'exited'
  AND harness = sqlc.arg(expected_source_harness)
  AND controller_generation = sqlc.arg(expected_source_controller_generation)
  AND activity_last_at <= sqlc.arg(activated_at);
```

Populate `ExpectedSourceControllerGeneration` from the admitted source record, keep `ControllerGeneration` equal to `started.ControllerGeneration`, and make both read-back ownership predicates compare source and target generations in their appropriate branches.

- [ ] **Step 4: Regenerate sqlc and run the complete affected packages**

Run: `npm run sqlc`

Run: `cd backend && go test ./internal/storage/sqlite/store ./internal/service/chat ./internal/session_manager ./internal/lifecycle -count=1`

Expected: PASS, including response-loss read-back where either the complete target tuple or the complete source tuple is proven.

- [ ] **Step 5: Commit the correctness prerequisite**

```bash
git add backend/internal/domain/agent_switching.go backend/internal/storage/sqlite/queries/agent_switching.sql backend/internal/storage/sqlite/gen/agent_switching.sql.go backend/internal/storage/sqlite/store/agent_switching_store.go backend/internal/storage/sqlite/store/agent_switching_store_test.go backend/internal/service/chat/controller_test.go backend/internal/session_manager/agent_switching_chat.go backend/internal/session_manager/agent_switching_test.go
git commit -m "fix: fence chat switch activation by source generation"
```

### Task 2: Make Runtime Ownership Proof Three-State and Retain Ambiguity

**Files:**

- Modify: `backend/internal/ports/outbound.go`
- Modify: `backend/internal/adapters/runtime/runtimeselect/runtimeselect.go`
- Modify: `backend/internal/adapters/runtime/conpty/ptyregistry/registry.go`
- Modify: `backend/internal/adapters/runtime/conpty/runtime.go`
- Modify: `backend/internal/adapters/runtime/tmux/tmux.go`
- Modify: `backend/internal/session_manager/manager.go` (`runtimeController`)
- Modify: `backend/internal/session_manager/agent_switching.go` (`reconcileStoppingSource`, `reconcileStartingTarget`, create/cleanup classification)
- Modify: `backend/internal/domain/agent_switching.go` and `backend/internal/storage/sqlite/store/agent_switching_store.go` (marker validation)
- Modify: `frontend/src/renderer/hooks/useAgentSwitches.ts`
- Test: `backend/internal/adapters/runtime/conpty/ptyregistry/ptyregistry_test.go`
- Test: `backend/internal/adapters/runtime/conpty/runtime_test.go`
- Test: `backend/internal/adapters/runtime/tmux/tmux_test.go`
- Test: `backend/internal/session_manager/agent_switching_test.go`
- Test: `frontend/src/renderer/hooks/useAgentSwitches.test.ts`

**Interfaces:**

- Consumes: persisted session handle, exact AO session, source/target generation, native identity when available, and adapter evidence.
- Produces: `ProbeFencedRuntime(context.Context, ports.FencedRuntimeRef) ports.FencedProbeResult`, which returns only `FencedAlive`, `FencedDead`, or `FencedUnknown`; and `ports.RuntimeEffectError`, which preserves partial-create handle/effect/cleanup evidence.

- [ ] **Step 1: Write failing adapter, recovery, marker, and polling tests**

```go
func TestReconcileStoppingSourceUnknownRetainsMarker(t *testing.T) {
	m := newSwitchManager(t)
	m.runtime.(*fakeRuntime).fencedResult = ports.FencedProbeResult{
		Liveness: ports.FencedUnknown, Reason: ports.FencedReasonRegistryUnreadable,
	}
	seedSwitch(t, m, domain.AgentSwitch{State: domain.AgentSwitchStoppingSource})

	resolved, err := m.reconcileRetainedAgentSwitchOnce(context.Background(), m.store.(ports.AgentSwitchStore), "session-1")
	require.Error(t, err)
	require.False(t, resolved)
	got := requireSwitch(t, m, "switch-1")
	require.Equal(t, domain.AgentSwitchStoppingSource, got.State)
	require.Equal(t, domain.AgentSwitchErrorSourceStopUnconfirmed, got.ErrorCode)
}
```

```ts
it("polls target-start recovery until ownership is resolved", () => {
	expect(agentSwitchesRefetchInterval([
		switchRecord({ state: "starting_target", errorCode: "target_start_unconfirmed" }),
	])).toBe(1_000);
});
```

Also pin: complete ConPTY scan + absent exact entry = dead; read/parse/write/permission failure = unknown; tmux exact process match = alive; ambiguous command/identity = unknown; partial target create + cleanup failure exposes nonempty evidence and retains the gate; failed-row validation rejects all three retained marker codes.

- [ ] **Step 2: Run tests and verify binary liveness/current recovery behavior fails**

Run: `cd backend && go test ./internal/adapters/runtime/conpty/... ./internal/adapters/runtime/tmux ./internal/session_manager -run 'Fenced|Registry.*Unknown|PartialCreate|SourceStopUnconfirmed' -count=1`

Run: `npm --prefix frontend test -- --run src/renderer/hooks/useAgentSwitches.test.ts`

Expected: FAIL because registry parsing collapses to empty/dead, recovery uses boolean probes, and target recovery is excluded from polling.

- [ ] **Step 3: Add the exact three-state and structured-effect contracts**

```go
type FencedLiveness string
const (
	FencedAlive FencedLiveness = "alive"
	FencedDead FencedLiveness = "dead"
	FencedUnknown FencedLiveness = "unknown"
)

type FencedProbeReason string
const (
	FencedReasonExactMatch FencedProbeReason = "exact_match"
	FencedReasonExactAbsent FencedProbeReason = "exact_absent"
	FencedReasonIdentityMissing FencedProbeReason = "identity_missing"
	FencedReasonRegistryUnreadable FencedProbeReason = "registry_unreadable"
	FencedReasonRegistryMalformed FencedProbeReason = "registry_malformed"
	FencedReasonProbeFailed FencedProbeReason = "probe_failed"
	FencedReasonGenerationMismatch FencedProbeReason = "generation_mismatch"
)

type FencedRuntimeRef struct {
	Handle RuntimeHandle
	SessionID domain.SessionID
	Generation string
	NativeIdentity string
}
type FencedProbeResult struct { Liveness FencedLiveness; Reason FencedProbeReason }
type FencedRuntimeProber interface {
	ProbeFencedRuntime(context.Context, FencedRuntimeRef) FencedProbeResult
}

type RuntimeEffectError interface {
	error
	PossibleHandle() RuntimeHandle
	EffectOutcome() RuntimeEffectOutcome
	CleanupOutcome() RuntimeCleanupOutcome
}
```

Make `runtimeselect.Runtime` embed `ports.FencedRuntimeProber`. Change ConPTY registry reading to `Scan() (entries []Entry, complete bool, err error)` without silently discarding malformed elements. tmux and ConPTY return `Unknown` for every non-exact or fallible proof. Keep ordinary `IsAlive` for display/reaper behavior only.

- [ ] **Step 4: Use fenced results in recovery and preserve retained states**

Replace ownership decisions in `reconcileStoppingSource` and `reconcileStartingTarget` with exhaustive switches over `FencedLiveness`. `Unknown` must call `markSourceStopUnconfirmed`/`markTargetStartUnconfirmed`, retain the in-memory operation gate, and perform no target creation or source restoration. Update `agentSwitchesRefetchInterval` to poll `agentSwitchNeedsRecovery`, not only source recovery.

Run: `cd backend && go test ./internal/adapters/runtime/conpty/... ./internal/adapters/runtime/tmux ./internal/session_manager ./internal/storage/sqlite/store -count=1`

Run: `npm --prefix frontend test -- --run src/renderer/hooks/useAgentSwitches.test.ts`

Expected: PASS; running reconciliation twice leaves the same marker and causes no repeated side effect.

- [ ] **Step 5: Commit the ownership-proof prerequisite**

```bash
git add backend/internal/ports/outbound.go backend/internal/adapters/runtime/runtimeselect/runtimeselect.go backend/internal/adapters/runtime/conpty backend/internal/adapters/runtime/tmux backend/internal/session_manager/manager.go backend/internal/session_manager/agent_switching.go backend/internal/session_manager/agent_switching_test.go backend/internal/domain/agent_switching.go backend/internal/storage/sqlite/store/agent_switching_store.go frontend/src/renderer/hooks/useAgentSwitches.ts frontend/src/renderer/hooks/useAgentSwitches.test.ts
git commit -m "fix: preserve unknown agent switch ownership"
```

### Task 3: Define the Stable Taxonomy and Canonical Privacy Event

**Files:**

- Create: `backend/internal/domain/agent_switch_observability.go`
- Create: `backend/internal/domain/agent_switch_observability_test.go`
- Create: `backend/internal/ports/agent_switch_observability.go`
- Create: `test/fixtures/agent-switch-observability/envelope-v1.json`

**Interfaces:**

- Consumes: typed, enum-only switch facts plus trusted release/platform metadata and optional sanitized in-app frames.
- Produces: `ValidateAgentSwitchFault(AgentSwitchFault) error`, `AgentSwitchDedupeKey(AgentSwitchFault) string`, `AgentSwitchIssueFingerprint(AgentSwitchFault) []string`, and the provider-neutral observer/delivery types used by Tasks 4-9. The concrete `BuildAgentSwitchCanonicalEvent(AgentSwitchEventBuildInput) ([]byte, error)` implementation lives in the Sentry adapter and is injected into storage through the encoder port.

- [ ] **Step 1: Write exhaustive failing taxonomy, applicability, canonical-byte, and privacy tests**

```go
func TestAgentSwitchFailureTaxonomyIsComplete(t *testing.T) {
	for _, point := range AllAgentSwitchFailurePoints() {
		entry, ok := AgentSwitchFailureTaxonomy(point)
		require.Truef(t, ok, "missing taxonomy for %s", point)
		require.NotEmpty(t, entry.Subsystem)
		require.NotEmpty(t, entry.AllowedPhases)
		require.NotEmpty(t, entry.ClassifierCallsite)
		require.NotEmpty(t, entry.Title)
		require.NotEmpty(t, entry.RunbookAnchor)
		if point == AgentSwitchFailureOutboxDelivery || point == AgentSwitchFailureClassificationUnknown {
			require.True(t, entry.LocalOnly)
			require.Equal(t, AgentSwitchReportNotApplicable, entry.ReportKind)
		} else {
			require.False(t, entry.LocalOnly)
		}
	}
}

func TestCanonicalAgentSwitchEventHasNoLocalIdentifiers(t *testing.T) {
	raw := requireCanonicalEvent(t, completeSafeFaultFixture())
	for _, forbidden := range []string{
		"session-", "switch-", "/Users/", "C:\\\\Users\\", "prompt text",
		"provider-conversation", "runtime-handle", "idempotency", "https://",
	} {
		require.NotContains(t, string(raw), forbidden)
	}
}
```

The test's expected point slice must list every approved value: admission, preflight/handoff, source stop, artifact/context, TUI and Chat target start, delivery, compensation/recovery, process/maintenance/visibility, `outbox_delivery`, and `classification_unknown`. Add a golden assertion that canonical bytes stay identical across map iteration, process restart, and release metadata changes after construction.

- [ ] **Step 2: Run the domain tests and verify the vocabulary does not exist**

Run: `cd backend && go test ./internal/domain -run 'AgentSwitchFailure|CanonicalAgentSwitch|AgentSwitchDedupe' -count=1`

Expected: FAIL with undefined taxonomy and event-builder symbols.

- [ ] **Step 3: Add all stable enums and exact cross-task data types**

```go
type AgentSwitchReportKind string
const (
	AgentSwitchReportTerminalFailure AgentSwitchReportKind = "terminal_failure"
	AgentSwitchReportRecoveryRequired AgentSwitchReportKind = "recovery_required"
	AgentSwitchReportPanic AgentSwitchReportKind = "panic"
	AgentSwitchReportRecoveryAttemptFailed AgentSwitchReportKind = "recovery_attempt_failed"
	AgentSwitchReportMaintenanceFailure AgentSwitchReportKind = "maintenance_failure"
	AgentSwitchReportDaemonLifecycleFailure AgentSwitchReportKind = "daemon_lifecycle_failure"
	AgentSwitchReportVisibilityFailure AgentSwitchReportKind = "visibility_failure"
	AgentSwitchReportNotApplicable AgentSwitchReportKind = "not_applicable"
)

type AgentSwitchFaultCode string
const (
	AgentSwitchFaultNotApplicable AgentSwitchFaultCode = "not_applicable"
	AgentSwitchFaultWorkerPanic AgentSwitchFaultCode = "worker_panic"
	AgentSwitchFaultRecoveryUnresolved AgentSwitchFaultCode = "recovery_unresolved"
	AgentSwitchFaultTerminalCleanupFailed AgentSwitchFaultCode = "terminal_cleanup_failed"
	AgentSwitchFaultShutdownWorkersTimedOut AgentSwitchFaultCode = "shutdown_workers_timed_out"
)

type AgentSwitchCallOutcome string
const (
	AgentSwitchCallOK AgentSwitchCallOutcome = "ok"
	AgentSwitchCallExpectedRejection AgentSwitchCallOutcome = "expected_rejection"
	AgentSwitchCallNoEffectFailure AgentSwitchCallOutcome = "no_effect_failure"
	AgentSwitchCallCommittedResponseLost AgentSwitchCallOutcome = "committed_response_lost"
	AgentSwitchCallEffectUnknown AgentSwitchCallOutcome = "effect_unknown"
	AgentSwitchCallStale AgentSwitchCallOutcome = "stale"
	AgentSwitchCallTimedOut AgentSwitchCallOutcome = "timed_out"
	AgentSwitchCallCancelled AgentSwitchCallOutcome = "cancelled"
	AgentSwitchCallPanic AgentSwitchCallOutcome = "panic"
	AgentSwitchCallCleanupFailed AgentSwitchCallOutcome = "cleanup_failed"
)

type AgentSwitchOwnership string
const (
	AgentSwitchOwnershipSource AgentSwitchOwnership = "source"
	AgentSwitchOwnershipNone AgentSwitchOwnership = "none"
	AgentSwitchOwnershipTarget AgentSwitchOwnership = "target"
	AgentSwitchOwnershipAmbiguous AgentSwitchOwnership = "ambiguous"
	AgentSwitchOwnershipNotApplicable AgentSwitchOwnership = "not_applicable"
)

type AgentSwitchCompensation string
const (
	AgentSwitchCompensationNotNeeded AgentSwitchCompensation = "not_needed"
	AgentSwitchCompensationSucceeded AgentSwitchCompensation = "succeeded"
	AgentSwitchCompensationFailed AgentSwitchCompensation = "failed"
	AgentSwitchCompensationUncertain AgentSwitchCompensation = "uncertain"
	AgentSwitchCompensationNotApplicable AgentSwitchCompensation = "not_applicable"
)

type AgentSwitchReportingAuthorization struct {
	Enabled bool
	ConsentGeneration string
	DestinationFingerprint string
}

type AgentSwitchEnrollmentStatus string
const (
	AgentSwitchEnrollmentEnrolled AgentSwitchEnrollmentStatus = "enrolled"
	AgentSwitchEnrollmentDisabled AgentSwitchEnrollmentStatus = "disabled"
	AgentSwitchEnrollmentStaleGeneration AgentSwitchEnrollmentStatus = "stale_generation"
	AgentSwitchEnrollmentDeduped AgentSwitchEnrollmentStatus = "deduped"
	AgentSwitchEnrollmentLocalInvariantFailed AgentSwitchEnrollmentStatus = "local_invariant_failed"
)
```

Define `AgentSwitchFailurePoint` constants for every exact string in the approved taxonomy, plus `AgentSwitchExecution` (`live`, `startup_reconcile`, `explicit_recovery`, `daemon_shutdown`), safe `AgentSwitchUserImpact`, `AgentSwitchSeverity`, `AgentSwitchRuntimeBackend`, `AgentSwitchClassifierCallsite`, and tri-state booleans (`true`, `false`, `not_applicable`). `AllAgentSwitchFailurePoints()` must return a copied sorted slice, never a mutable package slice.

- [ ] **Step 4: Build the fixed event struct and provider-neutral contracts**

```go
type AgentSwitchFault struct {
	ReportKind AgentSwitchReportKind
	FailurePoint AgentSwitchFailurePoint
	ClassifierCallsite AgentSwitchClassifierCallsite
	Phase AgentSwitchState
	ErrorCode AgentSwitchErrorCode
	FaultCode AgentSwitchFaultCode
	Execution AgentSwitchExecution
	ExecutionAttemptID string // local dedupe only
	Mode SessionMode
	FromHarness AgentHarness
	TargetHarness AgentHarness
	TargetStartMode AgentSwitchTargetStartMode
	RuntimeBackend AgentSwitchRuntimeBackend
	CallOutcome AgentSwitchCallOutcome
	Ownership AgentSwitchOwnership
	Compensation AgentSwitchCompensation
	UserImpact AgentSwitchUserImpact
	SourceStopConfirmed AgentSwitchTriState
	TargetOwnerCommitted AgentSwitchTriState
	GateRetained AgentSwitchTriState
	OccurredAt time.Time
	Frames []AgentSwitchStackFrame
}

type AgentSwitchEventBuildInput struct {
	EventID string
	Fault AgentSwitchFault
	Release string
	Environment string
	Channel string
	Platform string
	OS string
	ElapsedTimeBucket string
}

type AgentSwitchFailureEvent struct {
	EventID string
	EnvelopeEncodingVersion int
	CanonicalEventJSON []byte
}
```

Use a struct-only canonical JSON shape with fixed field order and UTC/RFC3339Nano. Do not marshal a tag or context map. Validate EventID, enums, applicability, frame fields, 16-KiB stack bound, and 60-KiB final JSON bound. The serialized exception is synthetic (`agent switch failure: <code> at <failure_point>`); raw errors are never accepted by the builder.

```go
type DeliveryOutcome string
const (
	DeliveryAccepted DeliveryOutcome = "accepted"
	DeliveryTransientFailure DeliveryOutcome = "transient_failure"
	DeliveryPermanentFailure DeliveryOutcome = "permanent_failure"
	DeliveryPolicyCancelled DeliveryOutcome = "policy_cancelled"
	DeliveryShutdownCancelled DeliveryOutcome = "shutdown_cancelled"
)
type DeliveryResult struct {
	Outcome DeliveryOutcome
	Class DeliveryErrorClass
	RetryNotBefore time.Time
	ThrottleScope DeliveryThrottleScope
}
type AgentSwitchFailureObserver interface {
	ObserveAgentSwitchFailure(context.Context, domain.AgentSwitchFailureEvent) DeliveryResult
}
```

Run: `cd backend && go test ./internal/domain ./internal/ports -count=1`

Expected: PASS with one taxonomy entry per enum and byte-stable privacy snapshots.

- [ ] **Step 5: Commit the typed observability boundary**

```bash
git add backend/internal/domain/agent_switch_observability.go backend/internal/domain/agent_switch_observability_test.go backend/internal/ports/agent_switch_observability.go test/fixtures/agent-switch-observability/envelope-v1.json
git commit -m "feat: define agent switch failure taxonomy"
```

### Task 4: Add the Durable Policy, Receipt, Outbox, and Atomic Store Contract

**Files:**

- Create: `backend/internal/storage/sqlite/migrations/0125_agent_switch_failure_observability.sql`
- Create: `backend/internal/storage/sqlite/queries/agent_switch_failure_observability.sql`
- Create: `backend/internal/storage/sqlite/store/agent_switch_failure_store.go`
- Create: `backend/internal/storage/sqlite/store/agent_switch_failure_store_test.go`
- Modify: `backend/internal/storage/sqlite/queries/agent_switching.sql`
- Modify: `backend/internal/storage/sqlite/store/agent_switching_store.go`
- Modify: `backend/internal/ports/agent_switching.go`
- Test: `backend/internal/storage/sqlite/migrate_agent_switching_schema_test.go`
- Modify: `backend/internal/storage/sqlite/migrate_burned_versions_test.go` (append version 119 to the migration ledger)
- Regenerate: `backend/internal/storage/sqlite/gen/agent_switch_failure_observability.sql.go`, `backend/internal/storage/sqlite/gen/agent_switching.sql.go`, `backend/internal/storage/sqlite/gen/models.go`

**Interfaces:**

- Consumes: a CAS-fenced `AgentSwitchMutation`, optional validated `AgentSwitchFault`, and exact `AgentSwitchReportingAuthorization` captured from the policy coordinator immediately before the transaction.
- Produces: `ApplyAgentSwitchMutation`, `FailAgentSwitchIfUnacknowledgedWithFault`, `EnqueueAgentSwitchOperationalFault`, `EnqueueAgentSwitchDaemonFault`, and `AgentSwitchFailureOutboxStore`; callers use `CoreChanged` for saga behavior and never branch product behavior on `Enrollment`.

- [ ] **Step 1: Write failing migration, atomicity, race, retention, and deletion tests**

```go
func TestFailedMutationAndOutboxCommitAtomically(t *testing.T) {
	st := openAgentSwitchStore(t)
	enableFailurePolicy(t, st, "generation-1", "destination-1")
	result, err := st.ApplyAgentSwitchMutation(context.Background(), ports.AgentSwitchMutation{
		Record: failedSwitchFixture(), ExpectedState: domain.AgentSwitchStartingTarget,
		ExpectedSourceGenerationID: "source-1", ExpectedTargetGenerationID: "target-1",
		Fault: ptr(terminalFaultFixture()),
		Authorization: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation-1", DestinationFingerprint: "destination-1"},
	})
	require.NoError(t, err)
	require.True(t, result.CoreChanged)
	require.Equal(t, domain.AgentSwitchEnrollmentEnrolled, result.Enrollment)
	require.Equal(t, 1, countOutbox(t, st))
	require.Equal(t, 1, countReceipts(t, st))
}
```

Add tests for: disabled/stale policy; zero-row CAS; repeated marker; acknowledgement-first and timeout-first; transaction rollback; an outbox INSERT abort trigger rolling back only the telemetry savepoint; ambiguous commit with no direct sender; session deletion before/after standalone enqueue; payload survival without an outbox foreign key; receipt cascade; unresolved receipt resolution; seven-day expiry; every payload status purged on opt-out; destination mismatch quarantine; and CDC/index preservation.

- [ ] **Step 2: Run the focused tests and verify schema/store symbols are absent**

Run: `cd backend && go test ./internal/storage/sqlite/... -run 'AgentSwitchFailure|FailedMutationAndOutbox|OutboxSavepoint|Receipt|FailurePolicy' -count=1`

Expected: FAIL because migration 0119 and failure-store operations do not exist.

- [ ] **Step 3: Add migration 0119 with the complete durable shape**

```sql
-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

-- Rebuild from the complete latest 0094/0103 shape because SQLite cannot
-- correct the existing state/error CHECK in place. Copy every current column,
-- add failure_point, replace the state/error CHECK exactly as specified below,
-- copy all rows, swap tables, and recreate every current index and trigger.

CREATE TABLE agent_switch_failure_policy (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    consent_generation TEXT NOT NULL,
    destination_fingerprint TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
INSERT INTO agent_switch_failure_policy VALUES (1, 0, '', '', CURRENT_TIMESTAMP);

CREATE TABLE agent_switch_failure_receipts (
    dedupe_key TEXT PRIMARY KEY,
    switch_id TEXT REFERENCES agent_switches(id) ON DELETE CASCADE,
    report_kind TEXT NOT NULL,
    durable_state_fingerprint TEXT NOT NULL,
    recorded_at TIMESTAMP NOT NULL,
    retain_until TIMESTAMP
);

CREATE TABLE agent_switch_failure_outbox (
    id TEXT PRIMARY KEY, schema_version INTEGER NOT NULL,
    envelope_encoding_version INTEGER NOT NULL, dedupe_key TEXT NOT NULL UNIQUE,
    destination_fingerprint TEXT NOT NULL, switch_id TEXT,
    report_kind TEXT NOT NULL, scope TEXT NOT NULL, failure_point TEXT NOT NULL,
    classifier_callsite TEXT NOT NULL, phase TEXT NOT NULL, error_code TEXT NOT NULL,
    fault_code TEXT NOT NULL, execution TEXT NOT NULL, execution_attempt_id TEXT NOT NULL,
    mode TEXT NOT NULL, from_harness TEXT NOT NULL, target_harness TEXT NOT NULL,
    target_start_mode TEXT NOT NULL, runtime_backend TEXT NOT NULL,
    call_outcome TEXT NOT NULL, ownership TEXT NOT NULL, compensation TEXT NOT NULL,
    user_impact TEXT NOT NULL, source_stop_confirmed TEXT NOT NULL,
    target_owner_committed TEXT NOT NULL, gate_retained TEXT NOT NULL,
    requested_at TIMESTAMP, occurred_at TIMESTAMP NOT NULL,
    sanitized_stack BLOB NOT NULL, stack_fingerprint TEXT NOT NULL,
    canonical_event_json BLOB NOT NULL CHECK (length(canonical_event_json) <= 61440),
    expires_at TIMESTAMP NOT NULL, available_at TIMESTAMP NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at TIMESTAMP,
    lease_token TEXT, lease_consent_generation TEXT, lease_delivery_epoch INTEGER,
    lease_expires_at TIMESTAMP, delivered_at TIMESTAMP, discarded_at TIMESTAMP,
    last_delivery_error_class TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_agent_switch_failure_outbox_pending
    ON agent_switch_failure_outbox(available_at, occurred_at)
    WHERE delivered_at IS NULL AND discarded_at IS NULL;

CREATE TABLE agent_switch_failure_delivery_state (
    destination_fingerprint TEXT PRIMARY KEY,
    error_not_before TIMESTAMP,
    all_not_before TIMESTAMP
);
PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd
```

Copy the exact current `agent_switches` definition from migration 0094, including every column and unrelated constraint. Add `failure_point TEXT NOT NULL DEFAULT ''`, copy every existing row with an empty point, and replace only its state/error constraint with: failed rows require a nonempty error other than `source_stop_unconfirmed`, `source_restore_unconfirmed`, or `target_start_unconfirmed`; `source_stop_unconfirmed` is valid only in `stopping_source`; `source_restore_unconfirmed` is valid only in `source_stopped` or `starting_target`; `target_start_unconfirmed` is valid only in `starting_target` with an empty target runtime handle; every other nonfailed row has an empty error. `delivery_unconfirmed` therefore remains a valid terminal failure. Recreate `idx_agent_switches_one_active_per_session`, `idx_agent_switches_session_history`, `agent_switches_target_native_scope_insert`, `agent_switches_target_native_scope_update`, `agent_switches_cdc_insert`, and `agent_switches_cdc_update`, using the latest 0103 CDC bodies. Add `failure_point` to every agent-switch query/projection and append migration 119 to the immutable migration ledger. The Down section is a documented no-op because rows may depend on the corrected constraint and safe downgrade is impossible. The schema test must prove failed rows reject all three retained markers, each valid nonterminal marker branch is accepted, current CDC triggers and indexes still exist with their latest definitions, and no store method writes `change_log`.

- [ ] **Step 4: Add the exact atomic and dispatcher-facing store APIs**

```go
type AgentSwitchMutation struct {
	Record domain.AgentSwitch
	ExpectedState domain.AgentSwitchState
	ExpectedSourceGenerationID domain.AgentGenerationID
	ExpectedTargetGenerationID domain.AgentGenerationID
	Fault *domain.AgentSwitchFault
	Authorization domain.AgentSwitchReportingAuthorization
}
type AgentSwitchMutationResult struct {
	CoreChanged bool
	Enrollment domain.AgentSwitchEnrollmentStatus
}

type AgentSwitchFaultStore interface {
	ApplyAgentSwitchMutation(context.Context, AgentSwitchMutation) (AgentSwitchMutationResult, error)
	FailAgentSwitchIfUnacknowledgedWithFault(context.Context, AgentSwitchMutation) (AgentSwitchMutationResult, error)
	EnqueueAgentSwitchOperationalFault(context.Context, AgentSwitchOperationalFault) (AgentSwitchMutationResult, error)
	EnqueueAgentSwitchDaemonFault(context.Context, AgentSwitchDaemonFault) (AgentSwitchMutationResult, error)
}
```

Before the core UPDATE, normalize an invalid point to `classification_unknown`. After `CoreChanged`, open `SAVEPOINT agent_switch_telemetry`, validate taxonomy/applicability, build immutable canonical JSON, then insert receipt and payload with `INSERT ... SELECT` guarded by the tx-bound enabled policy and exact consent generation/destination. On builder panic, constraint/shape failure, or serialization failure, roll back only the savepoint, log one safe local invariant, return `local_invariant_failed`, and commit the core mutation. Do not call any observer.

Define `AgentSwitchFailureOutboxStore` with exact methods `ForceDisableAgentSwitchFailurePolicy`, `ApplyAgentSwitchFailurePolicy`, `PurgeAgentSwitchFailurePayloads`, `EnrollCurrentAgentSwitchRecoveryMarkers`, `ClaimAgentSwitchFailure`, `BeginAgentSwitchFailureAttempt`, `SettleAgentSwitchFailureDelivery`, `ExpireAgentSwitchFailurePayloads`, `ResolveAgentSwitchFailureReceipts`, and `AgentSwitchFailureBacklog`. Claim/final-attempt predicates must include lease, generation, epoch, destination, throttle, and `expires_at > now`.

Run: `npm run sqlc`

Run: `cd backend && go test ./internal/storage/sqlite/... -count=1`

Expected: PASS, including injected telemetry-local failure where the saga state and `failure_point` commit but receipt/outbox counts stay zero.

- [ ] **Step 5: Commit the durable foundation**

```bash
git add backend/internal/storage/sqlite/migrations/0125_agent_switch_failure_observability.sql backend/internal/storage/sqlite/queries/agent_switch_failure_observability.sql backend/internal/storage/sqlite/queries/agent_switching.sql backend/internal/storage/sqlite/gen backend/internal/storage/sqlite/store/agent_switch_failure_store.go backend/internal/storage/sqlite/store/agent_switch_failure_store_test.go backend/internal/storage/sqlite/store/agent_switching_store.go backend/internal/storage/sqlite/migrate_agent_switching_schema_test.go backend/internal/storage/sqlite/migrate_burned_versions_test.go backend/internal/ports/agent_switching.go
git commit -m "feat: persist agent switch failure outbox"
```

### Task 5: Build the Acknowledged, Deterministic Sentry Envelope Sender

**Files:**

- Create: `backend/internal/observe/sentryobs/dsn.go`
- Create: `backend/internal/observe/sentryobs/agent_switch_sender.go`
- Create: `backend/internal/observe/sentryobs/agent_switch_sender_test.go`
- Modify: `test/fixtures/agent-switch-observability/envelope-v1.json`

**Interfaces:**

- Consumes: `ParseAgentSwitchDSN(raw string, production bool) (AgentSwitchDestination, error)` and immutable `domain.AgentSwitchFailureEvent`.
- Produces: `NewAgentSwitchFailureSender(AgentSwitchDestination, *http.Client) ports.AgentSwitchFailureObserver`; accepted means an actual 2xx response, never SDK enqueue/flush.

- [ ] **Step 1: Write failing parser, golden-envelope, status, redirect, bound, and throttle tests**

```go
func TestAgentSwitchSenderAcknowledgesOnlyProvider2xx(t *testing.T) {
	for _, tc := range []struct{ status int; want ports.DeliveryOutcome }{
		{202, ports.DeliveryAccepted}, {408, ports.DeliveryTransientFailure},
		{429, ports.DeliveryTransientFailure}, {503, ports.DeliveryTransientFailure},
		{400, ports.DeliveryPermanentFailure}, {302, ports.DeliveryPermanentFailure},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) { /* httptest server; assert tc.want */ })
	}
}
```

Test standard DSNs with base paths/effective ports; reject secret, query, fragment, malformed escaping, nonnumeric project, missing key/host, production HTTP, and non-loopback development HTTP. Assert no redirects/cookies, five-second context, 64-KiB request/response processing, `X-Sentry-Auth`, 32-lower-hex ID equality, byte-identical retries, and parsed `Retry-After`/`X-Sentry-Rate-Limits` including accepted 2xx throttles.

- [ ] **Step 2: Run sender tests and verify the dedicated transport is absent**

Run: `cd backend && go test ./internal/observe/sentryobs -run 'AgentSwitch|DSN|Envelope|RateLimit' -count=1`

Expected: FAIL with undefined parser/sender symbols.

- [ ] **Step 3: Implement strict destination parsing and version-routed encoding**

```go
type AgentSwitchDestination struct {
	Endpoint *url.URL
	PublicKey string
	ProjectID string
	Fingerprint string
}

func EncodeAgentSwitchEnvelopeV1(event domain.AgentSwitchFailureEvent) ([]byte, error) {
	if !eventIDPattern.MatchString(event.EventID) { return nil, ErrInvalidEventID }
	header := `{"event_id":"` + event.EventID + `"}` + "\n"
	item := `{"type":"event","length":` + strconv.Itoa(len(event.CanonicalEventJSON)) + `}` + "\n"
	out := append(append([]byte(header), []byte(item)...), event.CanonicalEventJSON...)
	if len(out) > 64<<10 { return nil, ErrEnvelopeTooLarge }
	return out, nil
}
```

Fingerprint SHA-256 over normalized scheme, host, effective port, base path, numeric project ID, and public key. Freeze encoder v1 against the shared fixture; dispatch by stored version and return permanent `unsupported_encoding` for unknown versions.

- [ ] **Step 4: Implement the five-second synchronous `net/http` observer**

Use a client with `CheckRedirect` returning `http.ErrUseLastResponse`, no cookie jar, default OS proxy/TLS behavior, bounded response headers/body, and request context deadline. Map timeout/network/408/429/5xx to transient, other 4xx/3xx to permanent, 2xx to accepted, and discard response bodies without logging them.

Run: `cd backend && go test ./internal/observe/sentryobs -count=1`

Expected: PASS, including response-lost-after-acceptance returning transient for a retry of identical bytes/EventID.

- [ ] **Step 5: Commit the acknowledged transport**

```bash
git add backend/internal/observe/sentryobs/dsn.go backend/internal/observe/sentryobs/agent_switch_sender.go backend/internal/observe/sentryobs/agent_switch_sender_test.go test/fixtures/agent-switch-observability/envelope-v1.json
git commit -m "feat: add acknowledged sentry envelope sender"
```

### Task 6: Establish One Crash-Durable Consent Authority Across Processes

**Files:**

- Create: `frontend/src/shared/telemetry-policy.ts`
- Create: `frontend/src/shared/telemetry-policy.test.ts`
- Create: `frontend/src/main/telemetry-policy-file.ts`
- Create: `frontend/src/main/telemetry-policy-file.test.ts`
- Create: `frontend/src/main/daemon-telemetry-policy-client.ts`
- Create: `frontend/src/main/daemon-telemetry-policy-client.test.ts`
- Create: `frontend/src/main/desktop-telemetry-controller.ts`
- Create: `frontend/src/main/desktop-telemetry-controller.test.ts`
- Create: `frontend/src/renderer/stores/telemetry-policy-store.ts`
- Create: `frontend/src/renderer/stores/telemetry-policy-store.test.ts`
- Create: `backend/internal/observe/agentswitch/policy.go`
- Create: `backend/internal/observe/agentswitch/policy_test.go`
- Modify: `frontend/src/shared/telemetry.ts`, `frontend/src/main.ts`, `frontend/src/main/sentry-main.ts`
- Modify: `frontend/src/preload.ts`, `frontend/src/preload.test.ts`, `frontend/src/renderer/lib/bridge.ts`
- Modify: `frontend/src/renderer/lib/sentry.ts`, `frontend/src/renderer/lib/telemetry.ts`, `frontend/src/renderer/main.tsx`
- Modify: `frontend/src/renderer/components/settings/GeneralSettingsSection.tsx`, `frontend/src/renderer/components/GlobalSettingsForm.test.tsx`
- Modify: `frontend/src/renderer/i18n/en.json`, `de.json`, `es.json`, `fr.json`, `ja.json`, `ko.json`, `pt-BR.json`, `zh-CN.json`
- Modify: `backend/internal/config/config.go`, `backend/internal/config/config_test.go`
- Modify: `backend/internal/httpd/router.go`, `backend/internal/httpd/control_test.go`, `backend/internal/httpd/lan_listener_test.go`
- Modify: `backend/internal/daemon/daemon.go`, `backend/internal/daemon/telemetry_wiring.go`, `backend/internal/observe/sentryobs/sentryobs.go`

**Interfaces:**

- Consumes: `AO_DATA_DIR/telemetry_policy.json`, environment hard vetoes, validated destination, release gate, and loopback daemon status.
- Produces: main `TelemetryPolicyAuthority` plus serialized `DesktopTelemetryController`, a renderer `TelemetryPolicyView` and settings control, daemon `agentswitch.PolicyCoordinator`, typed IPC bootstrap/capture methods, `/internal/agent-switch-observability/prepare-disable` and `/apply-policy`, and `Authorization() domain.AgentSwitchReportingAuthorization`.

- [ ] **Step 1: Write failing file-durability, truth-table, generation, opt-out, route, and sole-sender tests**

```ts
it("acknowledges a generation only after durable replace", async () => {
	const fs = faultingPolicyFS("after-rename-before-directory-sync");
	const authority = new TelemetryPolicyAuthority({ dataDir, fs, packagedDefault: true });
	await expect(authority.setEventsEnabled(false)).rejects.toThrow();
	expect(authority.snapshot().eventsEnabled).toBe(false);
	expect(authority.snapshot().acknowledged).toBe(false);
});
```

```go
func TestHeadlessAuthorityTruthTable(t *testing.T) {
	// valid off always off; valid on requires explicit env on; missing+explicit on
	// uses a boot token; missing without on and every malformed/unsafe case are off.
}
```

Also assert stale renderer/main/daemon generations are rejected; forged apply body cannot enable an off file; `/internal/*` stays unavailable on LAN; missing `prepare-disable` is caught by pre-claim/pre-send reads plus watcher; opt-out remains pending without daemon acknowledgement; and renderer modules contain no `@sentry/electron/renderer`, DSN, or network sender.

- [ ] **Step 2: Run focused backend/frontend tests and confirm eager init/current bootstrap fail**

Run: `cd backend && go test ./internal/observe/agentswitch ./internal/httpd ./internal/config ./internal/daemon -run 'Policy|Headless|PrepareDisable|ApplyPolicy|LAN' -count=1`

Run: `npm --prefix frontend test -- --run src/shared/telemetry-policy.test.ts src/main/telemetry-policy-file.test.ts src/main/daemon-telemetry-policy-client.test.ts src/main/desktop-telemetry-controller.test.ts src/preload.test.ts src/renderer/stores/telemetry-policy-store.test.ts src/renderer/lib/telemetry.init.test.ts src/renderer/lib/telemetry.test.ts src/renderer/components/GlobalSettingsForm.test.tsx`

Expected: FAIL because `initMainSentry` currently runs before policy bootstrap and renderer owns a Sentry SDK.

- [ ] **Step 3: Implement the 0600 crash-durable authority and bootstrap**

```ts
export type TelemetryPolicyDiskRecord = {
	schema_version: 1;
	events_enabled: boolean;
	consent_generation: string;
	updated_at: string;
};
export type TelemetryPolicySnapshot = {
	eventsEnabled: boolean;
	consentGeneration: string;
	updatedAt: string;
	acknowledged: boolean;
};
```

Keep `shared/telemetry-policy.ts` pure: it owns only the exact snake_case disk wire type above, strict parser, key/version/generation/time validation, the separate camelCase `TelemetryPolicyView` (`applied`, `cleanup_pending`, or `cleanup_failed`), and fail-closed result types. Put all filesystem work in `main/telemetry-policy-file.ts`. Reject symlinks, non-regular files, and non-owner-only permissions. Write a same-directory `wx` temporary file at 0600 and sync it before replace. On POSIX, rename then sync the containing directory. Do not claim ordinary Node `rename` is an equivalent Windows write-through replace: until a concrete `ReplaceFileW`/`MoveFileExW(MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH)` helper is shipped and tested, explicitly mark Windows policy durability unsupported, keep all Sentry surfaces disabled, reject live setting acknowledgement, and surface that release blocker locally. Add a win32 test for this fail-closed gate. First packaged launch writes the current packaged default only on a supported durable platform; an existing invalid file fails closed and is never replaced by an enabled default. Resolve one absolute desktop data directory before daemon launch—including relative `AO_DATA_DIR` against the daemon launch working directory—and pass that exact path to authority code, bootstrap, and daemon environment. Extend `TelemetryBootstrap` with `eventsEnabled` and `consentGeneration`.

- [ ] **Step 4: Implement daemon mirror/gate and ordered live changes**

```go
type PolicyCoordinator interface {
	ForceDisabled(context.Context) error
	Synchronize(context.Context) error
	PrepareDisable(context.Context) error
	ApplyPolicy(context.Context, string, bool) error
	Authorization() domain.AgentSwitchReportingAuthorization
	EnterDelivery(context.Context, string, int64) (context.Context, func(), bool)
	CloseAndDrain(context.Context) error
}
```

Startup order is force SQLite disabled → read/validate authority/headless truth table → mirror exact generation/destination → start dispatcher → reconcile. Before every claim and gate entry, synchronously reread the file; run a one-second watcher that closes the gate, increments only `delivery_epoch`, cancels registered calls, and awaits them. `apply-policy` treats its body as a hint and re-reads the file. Add `TelemetryEventsExplicit` to config so headless missing-file opt-in is distinguishable from defaults.

In Electron main, remove eager `void initMainSentry(...)`. Load/materialize policy first, then initialize the sole main transport and typed renderer intake. The daemon client accepts only the resolved loopback control origin and typed acknowledgements. Opt-out executes: close/drain main; attempt daemon prepare-disable; regardless of daemon absence, timeout, or failure, durably write exactly one off generation; then attempt daemon apply/purge; delete main cache and renderer queues; acknowledge only when the possibly live daemon has confirmed purge, otherwise expose `cleanup_pending` and retry. Test daemon-unavailable opt-out through a subsequent daemon synchronization. Re-enable first proves old purge completion, writes one enabled generation, applies it, and enrolls only unresolved markers lacking receipts. Main broadcasts every applied/pending policy view; preload keeps a mutable current generation from that trusted main broadcast, exposes it to renderer subscribers, and tags subsequent captures/signals with the latest value. Test disable then re-enable in the same still-open renderer without reload. The renderer store loads, subscribes, and changes this one main-owned preference through typed IPC; its General Settings control exposes saving, cleanup-pending, cleanup-failed, and environment-veto state without claiming enablement succeeded. Update all locale catalogs. Keep `const agentSwitchFailureProductionEnabled = false`; tests inject true.

Run: `cd backend && go test ./internal/observe/agentswitch ./internal/httpd ./internal/config ./internal/daemon -count=1`

Run: `npm --prefix frontend test -- --run src/shared/telemetry-policy.test.ts src/main/telemetry-policy-file.test.ts src/main/daemon-telemetry-policy-client.test.ts src/main/desktop-telemetry-controller.test.ts src/preload.test.ts src/renderer/stores/telemetry-policy-store.test.ts src/renderer/lib/telemetry.init.test.ts src/renderer/lib/telemetry.test.ts src/renderer/components/GlobalSettingsForm.test.tsx`

Expected: PASS with no new provider call after completed opt-out and no renderer-owned sender.

- [ ] **Step 5: Commit cross-process consent**

```bash
git add frontend/src/shared/telemetry-policy.ts frontend/src/shared/telemetry-policy.test.ts frontend/src/main/telemetry-policy-file.ts frontend/src/main/telemetry-policy-file.test.ts frontend/src/main/daemon-telemetry-policy-client.ts frontend/src/main/daemon-telemetry-policy-client.test.ts frontend/src/main/desktop-telemetry-controller.ts frontend/src/main/desktop-telemetry-controller.test.ts frontend/src/shared/telemetry.ts frontend/src/main.ts frontend/src/main/sentry-main.ts frontend/src/preload.ts frontend/src/preload.test.ts frontend/src/renderer/lib/bridge.ts frontend/src/renderer/lib/sentry.ts frontend/src/renderer/lib/telemetry.ts frontend/src/renderer/main.tsx frontend/src/renderer/stores/telemetry-policy-store.ts frontend/src/renderer/stores/telemetry-policy-store.test.ts frontend/src/renderer/components/settings/GeneralSettingsSection.tsx frontend/src/renderer/components/GlobalSettingsForm.test.tsx frontend/src/renderer/i18n backend/internal/observe/agentswitch/policy.go backend/internal/observe/agentswitch/policy_test.go backend/internal/config backend/internal/httpd backend/internal/daemon/daemon.go backend/internal/daemon/telemetry_wiring.go backend/internal/observe/sentryobs/sentryobs.go
git commit -m "feat: unify telemetry consent authority"
```

### Task 7: Run the Durable Dispatcher With Lease, TTL, Retry, and Throttle Semantics

**Files:**

- Create: `backend/internal/observe/agentswitch/dispatcher.go`
- Create: `backend/internal/observe/agentswitch/dispatcher_test.go`
- Modify: `backend/internal/daemon/telemetry_wiring.go`
- Modify: `backend/internal/daemon/daemon.go`

**Interfaces:**

- Consumes: `ports.AgentSwitchFailureOutboxStore`, `ports.AgentSwitchFailureObserver`, Task 6 `PolicyCoordinator`, clock/random seams, and wake channel.
- Produces: `Dispatcher.Start(context.Context) <-chan struct{}`, `Dispatcher.Wake()`, and `Dispatcher.Stop(context.Context) error`; exactly one daemon-lifetime instance starts before switch reconciliation.

- [ ] **Step 1: Write failing deterministic dispatcher tests**

```go
func TestDispatcherRetriesSameEventAfterAcceptedResponseLost(t *testing.T) {
	observer := &recordingObserver{results: []ports.DeliveryResult{
		{Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryResponseLost},
		{Outcome: ports.DeliveryAccepted},
	}}
	d := newDispatcherFixture(t, observer)
	d.runUntilIdle()
	require.Len(t, observer.events, 2)
	require.Equal(t, observer.events[0].EventID, observer.events[1].EventID)
	require.Equal(t, observer.envelopes[0], observer.envelopes[1])
}
```

Cover startup drain; claim/30-second reclaim; final CAS at expiry boundaries; call allowed to finish only if begun before TTL; backoff jitter bounds; Retry-After and category/all throttles across restart; accepted-response throttle; DSN rotation quarantine; policy/shutdown cancellation; opt-out purge; no-op observer refusal; and no saga ownership mutation or recursive outbox event.

- [ ] **Step 2: Run dispatcher tests and verify no worker exists**

Run: `cd backend && go test ./internal/observe/agentswitch -run 'Dispatcher|Lease|Retry|Throttle|TTL|Destination' -count=1`

Expected: FAIL with undefined `Dispatcher`.

- [ ] **Step 3: Implement one-row claim/send/settle cycles**

```go
type DispatcherConfig struct {
	Store ports.AgentSwitchFailureOutboxStore
	Observer ports.AgentSwitchFailureObserver
	Policy PolicyCoordinator
	Clock func() time.Time
	NewToken func() string
	Jitter func(time.Duration) time.Duration
	Logger *slog.Logger
}
```

Each cycle: reread authority and expire/quarantine → claim one due matching row without incrementing attempts → enter gate and revalidate authority/TTL → win final attempt CAS including lease/generation/epoch/destination/throttle/TTL → call observer outside SQLite → settle by matching lease. Accepted writes `delivered_at` and response throttle atomically; transient clears lease and sets the later of jitter/provider throttle; permanent quarantines; policy cancellation lets purge own the row. Ordinary shutdown stops new claims and lets an already-started bounded call settle; only a winning shutdown deadline cancels it and releases the lease.

- [ ] **Step 4: Wire lifecycle ordering and verify crash recovery**

Start dispatcher after store/policy/sender and before `ReconcileBackground`. On shutdown stop new claims, allow the at-most-one provider call to finish within its existing five-second bound, settle it when it finishes, and cancel/release the lease only if the outer shutdown deadline wins; then wait for switch workers. Opt-out remains different: it closes the delivery gate immediately and cancels/awaits every registered call. Test both orderings. Never start a no-op observer when an enabled policy or pending row exists.

Run: `cd backend && go test ./internal/observe/agentswitch ./internal/daemon ./internal/storage/sqlite/store -count=1`

Expected: PASS; two dispatcher processes cannot both send from one active lease, and a crash-reclaimed accepted row keeps the same EventID.

- [ ] **Step 5: Commit the delivery pipeline**

```bash
git add backend/internal/observe/agentswitch/dispatcher.go backend/internal/observe/agentswitch/dispatcher_test.go backend/internal/daemon/telemetry_wiring.go backend/internal/daemon/daemon.go
git commit -m "feat: dispatch durable agent switch failures"
```

### Task 8: Instrument Live Admission, TUI, and Chat Decisions

**Files:**

- Create: `backend/internal/session_manager/agent_switch_faults.go`
- Create: `backend/internal/session_manager/agent_switch_faults_test.go`
- Modify: `backend/internal/session_manager/manager.go`
- Modify: `backend/internal/session_manager/agent_switching.go`
- Modify: `backend/internal/session_manager/agent_switching_chat.go`
- Modify: `backend/internal/daemon/lifecycle_wiring.go`
- Test: `backend/internal/session_manager/agent_switching_test.go`

**Interfaces:**

- Consumes: Task 3 taxonomy, Task 4 typed store, Task 6 authorization snapshot, and typed adapter outcomes from Task 2.
- Produces: one enum-only `agentSwitchFlightRecorder` per execution and `settleAgentSwitchFault(context.Context, *domain.AgentSwitch, domain.AgentSwitchFault) (ports.AgentSwitchMutationResult, error)`; a completed switch discards the recorder.

- [ ] **Step 1: Write failing fault-injection and zero-success-event tests**

```go
func TestSuccessfulAgentSwitchCreatesNoFailureRows(t *testing.T) {
	m, st := newObservedSwitchManager(t, reportingEnabled())
	require.NoError(t, completeAgentSwitch(t, m))
	require.Equal(t, 0, st.failureReceiptCount())
	require.Equal(t, 0, st.failureOutboxCount())
}

func TestTargetCreateUnknownClassifiesExactBoundary(t *testing.T) {
	m, st := newObservedSwitchManager(t, reportingEnabled())
	m.runtime.(*fakeRuntime).createErr = runtimeEffectError(ports.RuntimeEffectUnknown, ports.RuntimeCleanupUncertain)
	_, err := m.SwitchAgent(context.Background(), "session-1", validSwitchConfig())
	require.NoError(t, err) // admission remains async
	m.waitSwitchWorkers(t)
	row := st.onlyFailurePayload()
	require.Equal(t, domain.AgentSwitchFailureTargetRuntimeCreate, row.FailurePoint)
	require.Equal(t, domain.AgentSwitchOwnershipAmbiguous, row.Ownership)
}
```

Add a table for every live admission/preflight/handoff/source-stop/artifact/TUI target/Chat target/delivery/compensation boundary, with `no_effect_failure`, `committed_response_lost`, `effect_unknown`, stale, timeout, cancellation, panic, and cleanup failure each exercised. Verify successful optional degradation, idempotent replay, expected rejection, winning acknowledgement, and read-back proof emit nothing.

- [ ] **Step 2: Run the live manager tests and verify current raw-error settlement lacks typed faults**

Run: `cd backend && go test ./internal/session_manager -run 'AgentSwitch.*Failure|SuccessfulAgentSwitchCreatesNoFailure|TargetCreateUnknown' -count=1`

Expected: FAIL because `failAgentSwitch` and marker helpers do not carry `failure_point` or enrollment input.

- [ ] **Step 3: Add the recorder and one typed transition adapter**

```go
type agentSwitchFlightRecorder struct {
	failurePoint domain.AgentSwitchFailurePoint
	lastDurablePhase domain.AgentSwitchState
	callOutcome domain.AgentSwitchCallOutcome
	ownership domain.AgentSwitchOwnership
	compensation domain.AgentSwitchCompensation
	userImpact domain.AgentSwitchUserImpact
	sourceStopConfirmed domain.AgentSwitchTriState
	targetOwnerCommitted domain.AgentSwitchTriState
	targetOwnershipAmbiguous bool
	gateRetained domain.AgentSwitchTriState
	execution domain.AgentSwitchExecution
}
```

It must contain no error/string content, IDs, paths, payloads, or provider facts. Add `ReportingPolicy ports.AgentSwitchReportingPolicy` to `sessionmanager.Deps`; read `Authorization()` immediately before each store transaction. Refactor `advanceAgentSwitch`, `failAgentSwitch`, `markSourceStopUnconfirmed`, `markSourceRestoreUnconfirmed`, `markTargetStartUnconfirmed`, and delivery failure to use `ApplyAgentSwitchMutation`/`FailAgentSwitchIfUnacknowledgedWithFault`. Ordinary progress/completed pass `Fault:nil`.

- [ ] **Step 4: Map all live decision branches and prove exact suppression**

At each fallible boundary set a stable failure point before the call, replace it only when a later boundary becomes causal, and derive severity from the complete outcome tuple. The final defer chooses terminal failure versus retained marker using proven ownership/cleanup facts. Chat failure after target activation remains target-owned and never restores source. Worker-start refusal and post-admission handoff-arm failures use the same typed settlement; ambiguous create follows read-back ownership and never sends directly.

Run: `cd backend && go test ./internal/session_manager ./internal/storage/sqlite/store ./internal/daemon -count=1`

Expected: PASS; all winning live failure/first-marker CASs enroll once when enabled and every success/suppressed branch enrolls zero.

- [ ] **Step 5: Commit live instrumentation**

```bash
git add backend/internal/session_manager/agent_switch_faults.go backend/internal/session_manager/agent_switch_faults_test.go backend/internal/session_manager/manager.go backend/internal/session_manager/agent_switching.go backend/internal/session_manager/agent_switching_chat.go backend/internal/session_manager/agent_switching_test.go backend/internal/daemon/lifecycle_wiring.go
git commit -m "feat: classify live agent switch failures"
```

### Task 9: Instrument Recovery, Panic, Maintenance, Acknowledgement, and Shutdown

**Files:**

- Modify: `backend/internal/session_manager/agent_switching.go`
- Modify: `backend/internal/session_manager/agent_switching_chat.go`
- Modify: `backend/internal/lifecycle/manager.go`
- Modify: `backend/internal/daemon/daemon.go`
- Test: `backend/internal/session_manager/agent_switch_faults_test.go`
- Test: `backend/internal/session_manager/agent_switching_test.go`
- Test: `backend/internal/lifecycle/manager_test.go`
- Test: `backend/internal/daemon/wiring_test.go`

**Interfaces:**

- Consumes: durable switch fingerprint/revision and Task 4 standalone enqueue operations.
- Produces: one semantic incident when reconciliation changes state; otherwise cardinality-fenced `panic`, `recovery_attempt_failed`, `maintenance_failure`, or daemon-level `shutdown_workers_timed_out`.

- [ ] **Step 1: Write failing restart-matrix, panic, acknowledgement-race, cleanup, and shutdown tests**

```go
func TestRecoveryPanicCreatesOneIncident(t *testing.T) {
	m, st := newObservedSwitchManager(t, reportingEnabled())
	seedRetainedSwitch(t, st)
	m.recoveryFault = func() { panic("must-not-be-exported") }
	m.runRecoveryTwice(t)
	require.Equal(t, 1, st.countKind(domain.AgentSwitchReportPanic))
	require.NotContains(t, string(st.onlyFailurePayload().CanonicalEventJSON), "must-not-be-exported")
}
```

Run each restart state twice; panic before/after source stop, target create, ownership commit, and inside recovery; both acknowledgement/timeout orders; duplicate/stale/wrong-generation acknowledgements; post-terminal cleanup; normal cancellation; and worker wait timeout. Assert panic plus resulting semantic CAS is one incident, not two.

- [ ] **Step 2: Run focused tests and verify panics/operational failures are log-only**

Run: `cd backend && go test ./internal/session_manager ./internal/lifecycle ./internal/daemon -run 'Reconcile|Panic|Acknowledg|Maintenance|ShutdownWorker' -count=1`

Expected: FAIL because recover handlers discard stack/cause and standalone faults are not enqueued.

- [ ] **Step 3: Preserve safe stacks and use standalone cardinality guards**

Capture `debug.Stack()` before converting a Chat panic. Sanitize to at most 16 KiB of repository-relative filename, line, package, and function; exclude panic value. The outer worker performs one bounded reconciliation. If that creates a terminal/marker classification, attach panic as its cause; otherwise call `EnqueueAgentSwitchOperationalFault` on a detached bounded context with the exact state/error/failure-point/updated-at fingerprint.

Use dedupe formulas from the spec for execution-attempt panic, unchanged recovery incident, daemon-run maintenance, and daemon-run shutdown. Post-terminal artifact cleanup enqueues once without changing terminal state. `WaitAgentSwitchWorkers` timeout calls `EnqueueAgentSwitchDaemonFault` once, not per switch; ordinary context cancellation is suppressed.

- [ ] **Step 4: Consume lifecycle acknowledgement booleans and close legacy seams**

Change `acknowledgeAgentSwitchTarget` to classify `changed=false` as duplicate/stale/timeout-won only after a read-back; it creates no report itself. After all reportable state mutations use typed methods, make legacy `UpdateAgentSwitch` and `FailAgentSwitchIfUnacknowledged` reject reportable transitions/markers while still permitting ordinary progress compatibility.

Run: `cd backend && go test ./internal/session_manager ./internal/lifecycle ./internal/daemon ./internal/storage/sqlite/store -count=1`

Expected: PASS; the second reconciliation and repeated operational call create no new receipt or payload.

- [ ] **Step 5: Commit recovery and process instrumentation**

```bash
git add backend/internal/session_manager/agent_switching.go backend/internal/session_manager/agent_switching_chat.go backend/internal/session_manager/agent_switch_faults_test.go backend/internal/session_manager/agent_switching_test.go backend/internal/lifecycle/manager.go backend/internal/lifecycle/manager_test.go backend/internal/daemon/daemon.go backend/internal/daemon/wiring_test.go backend/internal/storage/sqlite/store/agent_switching_store.go
git commit -m "feat: observe agent switch recovery failures"
```

### Task 10: Enforce Single Reporting Ownership Across Saga, HTTP, and Renderer

**Files:**

- Create: `backend/internal/observe/ownership/ownership.go`
- Create: `backend/internal/observe/ownership/ownership_test.go`
- Modify: `backend/internal/service/session/service.go`
- Modify: `backend/internal/httpd/envelope/envelope.go`
- Modify: `backend/internal/httpd/envelope/envelope_test.go`
- Modify: `backend/internal/httpd/log.go`, `backend/internal/httpd/log_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`
- Modify: `backend/internal/httpd/apispec/openapi.yaml`
- Modify: `frontend/src/api/schema.ts`
- Modify: `frontend/src/renderer/lib/api-client.ts`, `frontend/src/renderer/lib/api-client.test.ts`

**Interfaces:**

- Consumes: any error chain before/after durable saga admission.
- Produces: `ownership.OwnerHTTP`, `ownership.OwnerAgentSwitchSaga`, wrapping-safe `ownership.Own(err, owner)`, exact JSON/OpenAPI wire field `reporting_owner?: "http" | "agent_switch_saga"`, and duplicate suppression in HTTP and renderer.

- [ ] **Step 1: Write failing direct/wrapped/wire/renderer ownership tests**

```go
func TestOwnedErrorSurvivesMultipleWraps(t *testing.T) {
	err := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", ownership.Own(errors.New("boom"), ownership.OwnerAgentSwitchSaga)))
	require.Equal(t, ownership.OwnerAgentSwitchSaga, ownership.OwnerOf(err))
}
```

Test pre-admission unexpected 500 captures once in HTTP; post-admission synchronous 500 creates only durable saga payload while HTTP/renderer counters remain zero; serialization/deserialization preserves safe owner; normal UI error presentation remains unchanged.

- [ ] **Step 2: Run ownership tests and verify errors lose owner during mapping**

Run: `cd backend && go test ./internal/observe/ownership ./internal/service/session ./internal/httpd/... -run 'Owner|ReportingOwner|PostAdmission' -count=1`

Run: `npm --prefix frontend test -- --run src/renderer/lib/api-client.test.ts`

Expected: FAIL because `toAPIError` replaces mapped errors and the wire shape has no owner.

- [ ] **Step 3: Add the owner wrapper and preserve it around API mapping**

```go
type OwnedError struct { Err error; Owner Owner }
func (e *OwnedError) Error() string { return e.Err.Error() }
func (e *OwnedError) Unwrap() error { return e.Err }
func (e *OwnedError) ObservabilityOwner() Owner { return e.Owner }
```

Wrap every synchronous post-admission Manager error with saga ownership. Refactor `toAPIError` into `ownership.Preserve(original, mapSessionError(original))`, so `errors.As` still finds both owner and `*apierr.Error`. Extract only the enum into `APIError.ReportingOwner` and request-local metadata.

- [ ] **Step 4: Suppress duplicate capture and regenerate API artifacts**

In `requestLogger`, continue logging/rendering all 5xx responses but skip `sentryobs.CaptureHTTPError` for saga owner. In renderer `runtimeFetch`, parse the generated `reporting_owner` field; do not call generic Sentry capture for saga-owned responses. Run `npm run api`, then `cd backend && go test ./internal/httpd/... ./internal/service/session -count=1` and `npm --prefix frontend test -- --run src/renderer/lib/api-client.test.ts`.

Expected: PASS and `git diff --exit-code` after rerunning `npm run api` shows no generated drift.

- [ ] **Step 5: Commit ownership suppression**

```bash
git add backend/internal/observe/ownership backend/internal/service/session/service.go backend/internal/httpd/envelope backend/internal/httpd/log.go backend/internal/httpd/log_test.go backend/internal/httpd/controllers/dto.go backend/internal/httpd/apispec frontend/src/api/schema.ts frontend/src/renderer/lib/api-client.ts frontend/src/renderer/lib/api-client.test.ts
git commit -m "feat: assign agent switch reporting ownership"
```

### Task 11: Add Focus-Owned Frontend Visibility Failure Reporting

**Files:**

- Create: `frontend/src/shared/agent-switch-observability.ts`
- Create: `frontend/src/shared/agent-switch-observability.test.ts`
- Create: `frontend/src/main/agent-switch-observability.ts`, `frontend/src/main/agent-switch-observability.test.ts`
- Modify: `frontend/src/main/desktop-telemetry-controller.ts`, `frontend/src/main/desktop-telemetry-controller.test.ts`
- Create: `frontend/src/renderer/lib/agent-switch-visibility.ts`
- Create: `frontend/src/renderer/lib/agent-switch-visibility.test.ts`
- Create: `frontend/src/renderer/hooks/useAgentSwitchVisibility.ts`
- Create: `frontend/src/renderer/hooks/useAgentSwitchVisibility.test.tsx`
- Modify: `frontend/src/preload.ts`, `frontend/src/preload.test.ts`, `frontend/src/renderer/lib/bridge.ts`
- Modify: `frontend/src/renderer/global.d.ts`
- Modify: `frontend/src/renderer/lib/api-client.ts`
- Modify: `frontend/src/renderer/lib/event-transport.ts`, `frontend/src/renderer/lib/event-transport.test.ts`
- Modify: `frontend/src/renderer/types/workspace.ts`
- Modify: `frontend/src/renderer/hooks/useWorkspaceQuery.ts`, `frontend/src/renderer/hooks/useWorkspaceQuery.test.tsx`
- Modify: `frontend/src/renderer/hooks/useAgentSwitches.ts`
- Modify: `frontend/src/renderer/hooks/useObservedAgentSwitchLifecycle.ts`, `frontend/src/renderer/hooks/useObservedAgentSwitchLifecycle.test.ts`
- Modify: `frontend/src/renderer/lib/agent-switch-presentation.ts`, `frontend/src/renderer/lib/agent-switch-presentation.test.ts`
- Modify: `frontend/src/renderer/components/SessionView.tsx`, `frontend/src/renderer/components/SessionView.test.tsx`
- Modify: `frontend/src/renderer/components/chat/SessionChatSurface.tsx`
- Modify: `frontend/src/renderer/components/CenterPane.tsx`

**Interfaces:**

- Consumes: typed renderer signals `health`, `expected_presentation`, `presented`, `cancel`, `focus`, and `online`; local switch/revision/token never reaches event bytes.
- Produces: one main-owned state machine per local key (`healthy → suspect → reportable → reported → healthy`) and only `visibility_transport`, `visibility_query`, or `visibility_presentation` events through the current consent generation.

- [ ] **Step 1: Write failing fake-timer, multi-window, query/transport precedence, and presentation tests**

```ts
it("reports a still-current missing presentation after two seconds", async () => {
	const h = visibilityHarness({ focusedWindow: 1, online: true, consent: "generation-1" });
	h.expectedPresentation({
		token: "local-token", switchId: "local-switch", updatedAt: "2026-08-28T00:00:00Z",
		localRouteKey: "session/local-switch", presentationKind: "recovery_required",
		durableState: "starting_target",
	});
	await vi.advanceTimersByTimeAsync(2_000);
	expect(h.sent()).toMatchObject({ failurePoint: "visibility_presentation" });
	expect(JSON.stringify(h.sent())).not.toContain("local-switch");
});
```

Cover healed CDC/refresh, outage inside/beyond 15/60 seconds, query suppression while transport owns failure, two mounts, focus transfer, background/destroyed window, offline/blur/navigation/dismissal, layout-effect ack, stale token/revision, five healthy minutes, kill switch, restart generation, and normal failed-row rendering with zero visibility event.

- [ ] **Step 2: Run focused frontend tests and verify signals/state machine are absent**

Run: `npm --prefix frontend test -- --run src/shared/agent-switch-observability.test.ts src/main/agent-switch-observability.test.ts src/renderer/lib/agent-switch-visibility.test.ts src/renderer/hooks/useAgentSwitchVisibility.test.tsx`

Expected: FAIL with missing IPC/state-machine modules.

- [ ] **Step 3: Add typed IPC and main's focus-owned state machine**

```ts
export type AgentSwitchVisibilitySignalBody =
	| { kind: "transport"; operation: "active" | "history"; healthy: boolean; active: boolean }
	| { kind: "query"; operation: "active" | "history"; healthy: boolean; active: boolean }
	| { kind: "expected_presentation"; token: string; switchId: string; updatedAt: string;
		localRouteKey: string; presentationKind: "terminal_failure" | "recovery_required";
		durableState: "failed" | "stopping_source" | "source_stopped" | "starting_target" }
	| { kind: "presented" | "cancel"; token: string }
	| { kind: "focus" | "online"; value: boolean };

export type AgentSwitchVisibilitySignal = {
	consentGeneration: string;
	signal: AgentSwitchVisibilitySignalBody;
};
```

Renderer code sends only `AgentSwitchVisibilitySignalBody`; preload wraps every signal with the latest consent generation received through main's trusted policy-change broadcast, never a permanently frozen startup token and never a renderer-supplied generation. No payload may contain a sender/window identifier. Main derives the trusted sender from the IPC event's `event.sender.id`, rejects senders not registered as live AO shell webContents, selects the most recently focused eligible window, cancels old-owner timers before transfer, and rejects stale/disabled generations. `token`, `switchId`, `updatedAt`, and `localRouteKey` are local dedupe/validity inputs and must be stripped before encoding; only the bounded `presentationKind` and `durableState` enums reach the event. Snapshot-test the final event bytes for absence of every local value. Main applies the exact timers and sends a synthetic allowlisted envelope through its no-cache transport. `ao.agent_switch.visibility_failure` kill switch suppresses capture.

- [ ] **Step 4: Wire actual query, transport, and presentation boundaries**

`event-transport.ts` awaits its full workspace refresh after SSE failure and signals transport failure only when that refresh also fails; reconnect or a successful refresh signals healthy. `api-client.ts`, `useWorkspaceQuery`, and `useAgentSwitches` route typed workspace/switch-history failures to query visibility only while transport is healthy. Preserve generated `AgentSwitch.updatedAt` through the manual workspace projection so presentation expectations use a durable revision. `SessionView` registers route/focus/online ownership for both Chat and TUI. `useAgentSwitchVisibility` issues an expectation only for a current required failure/recovery component and acknowledges from `useLayoutEffect`; unmount, dismissal, navigation, blur, supersession, or route change cancels.

Run: `npm --prefix frontend test -- --run src/main/agent-switch-observability.test.ts src/main/desktop-telemetry-controller.test.ts src/renderer/lib/agent-switch-visibility.test.ts src/renderer/lib/event-transport.test.ts src/renderer/hooks/useWorkspaceQuery.test.tsx src/renderer/hooks/useAgentSwitchVisibility.test.tsx src/renderer/hooks/useAgentSwitches.test.ts src/renderer/hooks/useObservedAgentSwitchLifecycle.test.ts src/renderer/lib/agent-switch-presentation.test.ts src/renderer/components/SessionView.test.tsx src/renderer/components/CenterPane.test.tsx src/renderer/components/chat/SessionChatSurface.test.tsx`

Run: `npm run frontend:typecheck`

Expected: PASS with zero renderer network Sentry clients and zero duplicate daemon outcome events.

- [ ] **Step 5: Commit visibility-only reporting**

```bash
git add frontend/src/shared/agent-switch-observability.ts frontend/src/shared/agent-switch-observability.test.ts frontend/src/main/agent-switch-observability.ts frontend/src/main/agent-switch-observability.test.ts frontend/src/main/desktop-telemetry-controller.ts frontend/src/main/desktop-telemetry-controller.test.ts frontend/src/renderer/lib/agent-switch-visibility.ts frontend/src/renderer/lib/agent-switch-visibility.test.ts frontend/src/renderer/hooks/useAgentSwitchVisibility.ts frontend/src/renderer/hooks/useAgentSwitchVisibility.test.tsx frontend/src/preload.ts frontend/src/preload.test.ts frontend/src/renderer/lib/bridge.ts frontend/src/renderer/global.d.ts frontend/src/renderer/lib/api-client.ts frontend/src/renderer/lib/event-transport.ts frontend/src/renderer/lib/event-transport.test.ts frontend/src/renderer/types/workspace.ts frontend/src/renderer/hooks/useWorkspaceQuery.ts frontend/src/renderer/hooks/useWorkspaceQuery.test.tsx frontend/src/renderer/hooks/useAgentSwitches.ts frontend/src/renderer/hooks/useObservedAgentSwitchLifecycle.ts frontend/src/renderer/hooks/useObservedAgentSwitchLifecycle.test.ts frontend/src/renderer/lib/agent-switch-presentation.ts frontend/src/renderer/lib/agent-switch-presentation.test.ts frontend/src/renderer/components/SessionView.tsx frontend/src/renderer/components/SessionView.test.tsx frontend/src/renderer/components/chat/SessionChatSurface.tsx frontend/src/renderer/components/CenterPane.tsx
git commit -m "feat: report agent switch visibility failures"
```

### Task 12: Publish Runbooks, Prove Release Gates, and Raise the Single PR

**Files:**

- Create: `docs/runbooks/agent-switch-failure-points.md`
- Modify: `docs/telemetry.md`
- Modify: `docs/superpowers/specs/2026-08-28-agent-switch-failure-observability-design.md` only if implementation names differ, preserving approved semantics
- Test: all files touched by Tasks 1-11

**Interfaces:**

- Consumes: the compiled taxonomy and all automated verification from Tasks 1-11.
- Produces: complete operator documentation, explicit external privacy gate state, one green branch, and one PR against `main`; production reporting remains disabled.

- [ ] **Step 1: Write the runbook and telemetry disclosure, then review coverage**

For every `AllAgentSwitchFailurePoints()` value, document expected durable phase, safe ownership interpretation, whether source restoration is allowed, exact log message/tests, user recovery action, and release-blocker condition. `docs/telemetry.md` must disclose failure-only fields, seven-day payload TTL, unresolved payload-free receipts, current-marker opt-in behavior, at-least-once duplicates, source-IP/connection metadata, opt-out purge ordering, and the fact that successful switches emit nothing.

Use the compiled taxonomy's existing `RunbookAnchor` completeness test for the code contract, and review the human runbook as a table against that exported list; do not add a brittle test that parses prose or headings. Record Sentry organization IP storage/scrubbing, residency, retention, and automatic-context review as **not yet approved; production stream disabled** unless the reviewer supplies dated evidence during this PR.

- [ ] **Step 2: Run the complete targeted verification matrix**

Run: `npm run sqlc && git diff --exit-code -- backend/internal/storage/sqlite/gen`

Run: `npm run api && git diff --exit-code -- backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts`

Run: `cd backend && go test ./internal/domain ./internal/ports ./internal/storage/sqlite/... ./internal/observe/... ./internal/session_manager ./internal/lifecycle ./internal/httpd/... ./internal/daemon -count=1`

Run: `cd backend && go test -race ./internal/session_manager ./internal/observe/agentswitch ./internal/storage/sqlite/store -count=1`

Run: `npm --prefix frontend test -- --run src/shared/telemetry-policy.test.ts src/main/telemetry-policy-file.test.ts src/main/daemon-telemetry-policy-client.test.ts src/main/desktop-telemetry-controller.test.ts src/shared/agent-switch-observability.test.ts src/main/agent-switch-observability.test.ts src/preload.test.ts src/renderer/stores/telemetry-policy-store.test.ts src/renderer/lib/telemetry.init.test.ts src/renderer/lib/telemetry.test.ts src/renderer/lib/api-client.test.ts src/renderer/lib/agent-switch-visibility.test.ts src/renderer/lib/event-transport.test.ts src/renderer/hooks/useWorkspaceQuery.test.tsx src/renderer/hooks/useAgentSwitchVisibility.test.tsx src/renderer/hooks/useAgentSwitches.test.ts src/renderer/hooks/useObservedAgentSwitchLifecycle.test.ts src/renderer/lib/agent-switch-presentation.test.ts src/renderer/components/SessionView.test.tsx`

Run: `npm --prefix frontend test`

Run: `npm run frontend:typecheck`

Expected: every command exits 0; successful-switch counters are zero; generation/TTL/destination races are green under `-race`.

- [ ] **Step 3: Run repository gates and inspect the final diff**

Run: `npm run lint`

Run: `cd frontend && npm run build`

Run: `git diff --check && git status --short && git diff --stat main...HEAD`

Expected: checks pass; only intended implementation, generated artifacts, spec/plan, telemetry docs, fixtures, and runbook are present; `agentSwitchFailureProductionEnabled` remains false.

- [ ] **Step 4: Commit documentation and release-gate evidence**

```bash
git add docs/runbooks/agent-switch-failure-points.md docs/telemetry.md docs/superpowers/specs/2026-08-28-agent-switch-failure-observability-design.md
git commit -m "docs: add agent switch failure runbooks"
```

- [ ] **Step 5: Push and raise one implementation PR**

Run: `git push -u origin codex/agent-switch-failure-observability`

Run: `gh pr create --base main --head codex/agent-switch-failure-observability --title "feat: add failure-only observability for agent switching" --body "Implements the approved async agent-switch failure-observability design across correctness fencing, atomic SQLite enrollment, consent, acknowledged Sentry delivery, recovery, reporting ownership, and frontend visibility. Successful switches emit no reports. Production capture remains disabled until the documented Sentry privacy and runbook release gates are approved. Verification commands and rollout commits are included in the PR history."`

Expected: one PR URL targeting `main`; do not create phase-specific PRs and do not enable production capture as part of PR creation.
