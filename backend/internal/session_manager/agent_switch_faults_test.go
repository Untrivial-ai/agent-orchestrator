package sessionmanager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/ownership"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPreAdmissionFailureClassificationsRemainOutsideSagaOwnership(t *testing.T) {
	for _, point := range []domain.AgentSwitchFailurePoint{
		domain.AgentSwitchFailureSourceNativePreserve,
		domain.AgentSwitchFailureAdmissionSagaCreate,
		domain.AgentSwitchFailureAdmissionCommitReadback,
	} {
		err := classifyAgentSwitchAdmissionFailure(point, errors.New("admission failed"))
		var classified *agentSwitchAdmissionFailure
		if !errors.As(err, &classified) || classified.point != point {
			t.Fatalf("classification = %#v, want %q", classified, point)
		}
		if owner := ownership.OwnerOf(err); owner.Valid() {
			t.Fatalf("pre-admission point %q acquired owner %q", point, owner)
		}
	}
}

func TestRecorderCoversDeferredLiveCausalBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchSourceStopped, RequestedAt: now, UpdatedAt: now,
	}
	m := New(Deps{Clock: func() time.Time { return now }})
	for _, point := range []domain.AgentSwitchFailurePoint{
		domain.AgentSwitchFailureSourceControllerDrain,
		domain.AgentSwitchFailureSourceMetadataRefresh,
		domain.AgentSwitchFailureSemanticArtifactVerify,
		domain.AgentSwitchFailureSourceTranscriptCapture,
		domain.AgentSwitchFailureContinuationBuild,
		domain.AgentSwitchFailureFinalArtifactVerify,
		domain.AgentSwitchFailureTUITargetAckCommit,
	} {
		recorder := newAgentSwitchFlightRecorder(sw, domain.SessionModeTUI, domain.AgentSwitchExecutionLive)
		recorder.boundary(point)
		fault := m.faultFromRecorder(sw, domain.AgentSwitchErrorFailedPostStop, domain.AgentSwitchReportTerminalFailure, recorder)
		if fault.FailurePoint != point {
			t.Fatalf("fault point = %q, want %q", fault.FailurePoint, point)
		}
		entry, ok := domain.AgentSwitchFailureTaxonomy(point)
		if !ok || fault.ClassifierCallsite != entry.ClassifierCallsite {
			t.Fatalf("fault classifier = %q, taxonomy = %+v", fault.ClassifierCallsite, entry)
		}
	}
}

type staticAgentSwitchReportingPolicy struct {
	authorization domain.AgentSwitchReportingAuthorization
}

func (p staticAgentSwitchReportingPolicy) Authorization() domain.AgentSwitchReportingAuthorization {
	return p.authorization
}

func TestAgentSwitchFlightRecorderContainsOnlyClosedObservabilityFacts(t *testing.T) {
	typeOfRecorder := reflect.TypeOf(agentSwitchFlightRecorder{})
	for i := 0; i < typeOfRecorder.NumField(); i++ {
		field := typeOfRecorder.Field(i)
		if field.Type.Kind() == reflect.Bool {
			continue
		}
		if field.Type.PkgPath() != reflect.TypeOf(domain.AgentSwitchFault{}).PkgPath() {
			t.Fatalf("field %s has non-domain type %s; recorder must not retain raw strings, errors, IDs, or provider facts", field.Name, field.Type)
		}
	}
}

func TestAdvanceAgentSwitchUsesTypedStoreWithoutFaultForSuccess(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-success", SessionID: "session-success",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	m := New(Deps{Store: store, Clock: func() time.Time { return now.Add(time.Second) }})

	if err := m.advanceAgentSwitch(context.Background(), store, &sw, domain.AgentSwitchStoppingSource, nil); err != nil {
		t.Fatalf("advanceAgentSwitch: %v", err)
	}
	if len(store.faultMutations) != 1 {
		t.Fatalf("typed mutations = %d, want 1", len(store.faultMutations))
	}
	if store.faultMutations[0].Fault != nil {
		t.Fatalf("successful progress carried fault %+v", *store.faultMutations[0].Fault)
	}
}

func TestFailAgentSwitchClassifiesTypedBoundaryAndReadsAuthorizationAtSettlement(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-failure", SessionID: "session-failure",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	authorization := domain.AgentSwitchReportingAuthorization{
		Enabled: true, ConsentGeneration: "consent-generation", DestinationFingerprint: "destination-fingerprint",
	}
	m := New(Deps{
		Store: store, Clock: func() time.Time { return now.Add(time.Second) },
		ReportingPolicy: staticAgentSwitchReportingPolicy{authorization: authorization},
	})
	recorder := newAgentSwitchFlightRecorder(sw, domain.SessionModeTUI, domain.AgentSwitchExecutionLive)
	recorder.failurePoint = domain.AgentSwitchFailureTargetPreflight
	recorder.callOutcome = domain.AgentSwitchCallNoEffectFailure

	settled, err := m.failAgentSwitchWithRecorder(context.Background(), store, sw, domain.AgentSwitchErrorFailedPreStop, recorder)
	if err != nil {
		t.Fatalf("failAgentSwitchWithRecorder: %v", err)
	}
	if settled.State != domain.AgentSwitchFailed {
		t.Fatalf("state = %q, want failed", settled.State)
	}
	if len(store.faultMutations) != 1 || store.faultMutations[0].Fault == nil {
		t.Fatalf("typed fault mutations = %+v, want exactly one fault", store.faultMutations)
	}
	mutation := store.faultMutations[0]
	if mutation.Authorization != authorization {
		t.Fatalf("authorization = %+v, want %+v", mutation.Authorization, authorization)
	}
	if mutation.Fault.FailurePoint != domain.AgentSwitchFailureTargetPreflight || mutation.Fault.CallOutcome != domain.AgentSwitchCallNoEffectFailure {
		t.Fatalf("fault = %+v", *mutation.Fault)
	}
	if mutation.Record.FailurePoint != domain.AgentSwitchFailureTargetPreflight {
		t.Fatalf("durable failure point = %q", mutation.Record.FailurePoint)
	}
}

func TestFailAgentSwitchWinningAcknowledgementCreatesNoFault(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-ack", SessionID: "session-ack",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		TargetStartMode: domain.AgentSwitchTargetStartFresh,
		State:           domain.AgentSwitchDelivering, SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	store.ackBeforeDeliveryFailure = true
	m := New(Deps{Store: store, Clock: func() time.Time { return now.Add(time.Second) }})
	recorder := newAgentSwitchFlightRecorder(sw, domain.SessionModeTUI, domain.AgentSwitchExecutionLive)
	recorder.failurePoint = domain.AgentSwitchFailureTUITargetHookWait
	recorder.callOutcome = domain.AgentSwitchCallTimedOut
	recorder.sourceStopConfirmed = domain.AgentSwitchTriTrue
	recorder.targetOwnerCommitted = domain.AgentSwitchTriTrue
	recorder.ownership = domain.AgentSwitchOwnershipTarget
	recorder.userImpact = domain.AgentSwitchUserImpactDeliveryUnknown

	settled, err := m.failAgentSwitchWithRecorder(context.Background(), store, sw, domain.AgentSwitchErrorDeliveryUnconfirmed, recorder)
	if err != nil {
		t.Fatalf("failAgentSwitchWithRecorder: %v", err)
	}
	if settled.State != domain.AgentSwitchCompleted {
		t.Fatalf("state = %q, want completed", settled.State)
	}
	if len(store.faultMutations) != 2 {
		t.Fatalf("typed mutations = %d, want failure CAS plus fault-free completion", len(store.faultMutations))
	}
	if store.faultMutations[0].Fault == nil || store.faultMutations[1].Fault != nil {
		t.Fatalf("winning acknowledgement must leave only the losing CAS candidate and fault-free completion: %+v", store.faultMutations)
	}
}

var _ ports.AgentSwitchReportingPolicy = staticAgentSwitchReportingPolicy{}

func TestSanitizeAgentSwitchPanicStackExcludesValueAndBoundsFrames(t *testing.T) {
	raw := []byte("panic: must-not-be-exported\n\n" +
		"github.com/aoagents/agent-orchestrator/backend/internal/session_manager.(*Manager).executeAgentSwitch(0x1)\n" +
		"\t/Users/private/reverb/backend/internal/session_manager/agent_switching.go:417 +0x45\n" +
		"runtime.goexit()\n\t/usr/local/go/src/runtime/asm_amd64.s:1700 +0x1\n")
	frames := sanitizeAgentSwitchPanicStack(raw)
	if len(frames) != 1 {
		t.Fatalf("frames = %+v, want one in-app frame", frames)
	}
	if frames[0].Filename != "backend/internal/session_manager/agent_switching.go" || frames[0].Line != 417 {
		t.Fatalf("sanitized frame = %+v", frames[0])
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{frames[0].Package, frames[0].Function, frames[0].Filename}, "|")), "must-not-be-exported") {
		t.Fatalf("panic value escaped in frames: %+v", frames)
	}
}

func TestPanicAttachedToWinningSemanticMutationCreatesOneIncident(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-panic", SessionID: "session-panic",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	m := New(Deps{Store: store, Clock: func() time.Time { return now.Add(time.Second) }})
	recorder := newAgentSwitchFlightRecorder(sw, domain.SessionModeTUI, domain.AgentSwitchExecutionLive)
	ctx := withAgentSwitchPanicCause(context.Background(), agentSwitchPanicCause{
		failurePoint:     domain.AgentSwitchFailureLiveWorkerPanic,
		executionAttempt: "attempt-1",
		frames: []domain.AgentSwitchStackFrame{{
			Package: "session_manager", Function: "Manager.executeAgentSwitch",
			Filename: "backend/internal/session_manager/agent_switching.go", Line: 1,
		}},
	})

	settled, err := m.failAgentSwitchWithRecorder(ctx, store, sw, domain.AgentSwitchErrorFailedPreStop, recorder)
	if err != nil {
		t.Fatalf("failAgentSwitchWithRecorder: %v", err)
	}
	if settled.State != domain.AgentSwitchFailed || settled.ErrorCode != domain.AgentSwitchErrorFailedPreStop {
		t.Fatalf("semantic settlement = %+v", settled)
	}
	if len(store.faultMutations) != 1 || store.faultMutations[0].Fault == nil {
		t.Fatalf("fault mutations = %+v, want one", store.faultMutations)
	}
	fault := store.faultMutations[0].Fault
	if fault.ReportKind != domain.AgentSwitchReportPanic || fault.ErrorCode != domain.AgentSwitchErrorNotApplicable ||
		fault.FaultCode != domain.AgentSwitchFaultWorkerPanic || fault.ExecutionAttemptID != "attempt-1" {
		t.Fatalf("attached panic fault = %+v", *fault)
	}
	if len(store.operationalFaults) != 0 {
		t.Fatalf("attached panic also enqueued standalone faults: %+v", store.operationalFaults)
	}
}

func TestUnchangedRecoveryFailureEnqueuesExactFingerprintOncePerStoreDedupe(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-recovery", SessionID: "session-recovery",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		TargetStartMode: domain.AgentSwitchTargetStartFresh,
		State:           domain.AgentSwitchStartingTarget, ErrorCode: domain.AgentSwitchErrorTargetStartUnconfirmed,
		FailurePoint:       domain.AgentSwitchFailureTargetRuntimeCreate,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
		RequestedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	m := New(Deps{
		Store: store, Clock: func() time.Time { return now.Add(time.Second) },
		ReportingPolicy: staticAgentSwitchReportingPolicy{authorization: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation", DestinationFingerprint: "destination"}},
	})

	m.observeUnchangedAgentSwitchRecoveryFailure(context.Background(), store, sw, domain.SessionModeTUI, domain.AgentSwitchExecutionExplicitRecovery)
	if len(store.operationalFaults) != 1 {
		t.Fatalf("operational faults = %d, want 1", len(store.operationalFaults))
	}
	got := store.operationalFaults[0]
	if got.ExpectedState != sw.State || got.ExpectedErrorCode != sw.ErrorCode || got.ExpectedFailurePoint != sw.FailurePoint || !got.ExpectedUpdatedAt.Equal(sw.UpdatedAt) {
		t.Fatalf("standalone fingerprint = %+v, want exact switch fingerprint %+v", got, sw)
	}
	if got.Fault.ReportKind != domain.AgentSwitchReportRecoveryAttemptFailed || got.Fault.FailurePoint != domain.AgentSwitchFailureRecoveryExistingMarker {
		t.Fatalf("recovery fault = %+v", got.Fault)
	}
}

func TestNormalRecoveryCancellationDoesNotEnqueueOperationalFault(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-cancel", SessionID: "session-cancel",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		TargetStartMode: domain.AgentSwitchTargetStartFresh,
		State:           domain.AgentSwitchStartingTarget, ErrorCode: domain.AgentSwitchErrorTargetStartUnconfirmed,
		FailurePoint:       domain.AgentSwitchFailureTargetRuntimeCreate,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
		RequestedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	m := New(Deps{Store: store, Clock: func() time.Time { return now }})
	m.observeAgentSwitchRecoveryFailure(context.Background(), store, sw, domain.SessionModeTUI, domain.AgentSwitchExecutionStartupReconcile, context.Canceled)
	if len(store.operationalFaults) != 0 {
		t.Fatalf("normal cancellation enqueued %+v", store.operationalFaults)
	}
}

func TestTerminalCleanupFailureUsesDaemonScopedMaintenanceFingerprint(t *testing.T) {
	store := newSwitchTestStore()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "switch-maintenance", SessionID: "session-maintenance",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchCompleted, SourceGenerationID: "source-generation",
		RequestedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	store.switches[sw.ID] = sw
	m := New(Deps{Store: store, DaemonRunID: "daemon-run-1", Clock: func() time.Time { return now.Add(time.Second) }})
	m.observeTerminalAgentSwitchMaintenanceFailure(context.Background(), store, sw, domain.SessionModeTUI, domain.AgentSwitchExecutionStartupReconcile)
	if len(store.operationalFaults) != 1 {
		t.Fatalf("maintenance faults = %d, want 1", len(store.operationalFaults))
	}
	got := store.operationalFaults[0]
	if got.DaemonRunID != "daemon-run-1" || got.ExpectedState != domain.AgentSwitchCompleted ||
		got.Fault.ReportKind != domain.AgentSwitchReportMaintenanceFailure ||
		got.Fault.FailurePoint != domain.AgentSwitchFailureTerminalArtifactCleanup ||
		got.Fault.ErrorCode != domain.AgentSwitchErrorNotApplicable ||
		got.Fault.Execution != domain.AgentSwitchExecutionStartupReconcile {
		t.Fatalf("maintenance fault = %+v", got)
	}

	nonterminal := sw
	nonterminal.State = domain.AgentSwitchDelivering
	m.observeTerminalAgentSwitchMaintenanceFailure(context.Background(), store, nonterminal, domain.SessionModeTUI, domain.AgentSwitchExecutionLive)
	if len(store.operationalFaults) != 1 {
		t.Fatalf("nonterminal cleanup created a fault: %+v", store.operationalFaults)
	}
}
