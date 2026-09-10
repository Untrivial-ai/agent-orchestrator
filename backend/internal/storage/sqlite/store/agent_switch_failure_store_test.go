package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/sentryobs"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"

	_ "modernc.org/sqlite"
)

const failureStorePragmas = "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

type agentSwitchFailureFixture struct {
	store *sqlite.Store
	db    *sql.DB
	now   time.Time
	sw    domain.AgentSwitch
}

func openAgentSwitchFailureFixture(t *testing.T) agentSwitchFailureFixture {
	return openAgentSwitchFailureFixtureWithMetadata(t, true)
}

func testAgentSwitchFailureEventMetadata() domain.AgentSwitchEventMetadata {
	return domain.AgentSwitchEventMetadata{
		Release: "1.2.3", Environment: domain.AgentSwitchEnvironmentDevelopment,
		Channel: domain.AgentSwitchChannelPreview, Platform: domain.AgentSwitchPlatformDaemon,
		OS: domain.AgentSwitchOSLinux, ElapsedTimeBucket: domain.AgentSwitchElapsedNotApplicable,
	}
}

func openAgentSwitchFailureFixtureWithMetadata(t *testing.T, configureMetadata bool) agentSwitchFailureFixture {
	t.Helper()
	dataDir := t.TempDir()
	st, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+failureStorePragmas)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := st.ConfigureAgentSwitchFailureEventEncoder(context.Background(), sentryobs.AgentSwitchEventEncoder{}); err != nil {
		t.Fatalf("configure event encoder: %v", err)
	}

	now := time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC)
	if err := st.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: "failure-project", Path: "/tmp/failure-project", RegisteredAt: now,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	session, err := st.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID: "failure-project", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{WorkspacePath: "/tmp/failure-project"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if configureMetadata {
		if err := st.ConfigureAgentSwitchFailureEventMetadata(context.Background(), testAgentSwitchFailureEventMetadata()); err != nil {
			t.Fatalf("configure event metadata: %v", err)
		}
	}
	sw := domain.AgentSwitch{
		ID: "switch-1", SessionID: session.ID, IdempotencyKey: "switch-key-1",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceTranscriptStatus: domain.AgentSwitchSourceTranscriptNotAttempted,
		SourceGenerationID:     "source-1", RequestedAt: now, UpdatedAt: now,
	}
	if _, created, err := st.CreateAgentSwitch(context.Background(), sw); err != nil || !created {
		t.Fatalf("seed switch: created=%v err=%v", created, err)
	}
	return agentSwitchFailureFixture{store: st, db: db, now: now, sw: sw}
}

func (f agentSwitchFailureFixture) authorization() domain.AgentSwitchReportingAuthorization {
	return domain.AgentSwitchReportingAuthorization{
		Enabled: true, ConsentGeneration: "generation-1", DestinationFingerprint: "destination-1",
	}
}

func (f agentSwitchFailureFixture) enablePolicy(t *testing.T) {
	t.Helper()
	if err := f.store.ApplyAgentSwitchFailurePolicy(context.Background(), ports.AgentSwitchFailurePolicy{
		Authorization: f.authorization(), UpdatedAt: f.now,
	}); err != nil {
		t.Fatalf("enable failure policy: %v", err)
	}
}

func terminalFault(at time.Time) domain.AgentSwitchFault {
	return domain.AgentSwitchFault{
		ReportKind:           domain.AgentSwitchReportTerminalFailure,
		FailurePoint:         domain.AgentSwitchFailureWorkerStartRefused,
		ClassifierCallsite:   domain.AgentSwitchClassifierAdmission,
		Phase:                domain.AgentSwitchPreparingHandoff,
		ErrorCode:            domain.AgentSwitchErrorFailedPreStop,
		FaultCode:            domain.AgentSwitchFaultNotApplicable,
		Execution:            domain.AgentSwitchExecutionLive,
		Mode:                 domain.SessionModeTUI,
		FromHarness:          domain.HarnessClaudeCode,
		TargetHarness:        domain.HarnessCodex,
		TargetStartMode:      domain.AgentSwitchTargetStartReportedPending,
		RuntimeBackend:       domain.AgentSwitchRuntimeNotApplicable,
		CallOutcome:          domain.AgentSwitchCallNoEffectFailure,
		Ownership:            domain.AgentSwitchOwnershipSource,
		Compensation:         domain.AgentSwitchCompensationNotNeeded,
		UserImpact:           domain.AgentSwitchUserImpactSourceAvailable,
		SourceStopConfirmed:  domain.AgentSwitchTriFalse,
		TargetOwnerCommitted: domain.AgentSwitchTriFalse,
		GateRetained:         domain.AgentSwitchTriFalse,
		OccurredAt:           at,
	}
}

func failedMutation(f agentSwitchFailureFixture) ports.AgentSwitchMutation {
	rec := f.sw
	rec.State = domain.AgentSwitchFailed
	rec.ErrorCode = domain.AgentSwitchErrorFailedPreStop
	rec.FailurePoint = domain.AgentSwitchFailureWorkerStartRefused
	rec.UpdatedAt = f.now.Add(time.Second)
	fault := terminalFault(rec.UpdatedAt)
	return ports.AgentSwitchMutation{
		Record: rec, ExpectedState: domain.AgentSwitchPreparingHandoff,
		ExpectedSourceGenerationID: "source-1", ExpectedTargetGenerationID: "",
		Fault: &fault, Authorization: f.authorization(),
	}
}

func countFailureRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func deleteFailureFixtureSession(t *testing.T, f agentSwitchFailureFixture) {
	t.Helper()
	// change_log deliberately has a non-cascading session foreign key. Product
	// deletion clears those CDC rows before deleting the session, so mirror that
	// ordering when exercising payload independence.
	if _, err := f.db.Exec(`DELETE FROM change_log WHERE session_id=?`, f.sw.SessionID); err != nil {
		t.Fatalf("clear session change log: %v", err)
	}
	if _, err := f.db.Exec(`DELETE FROM sessions WHERE id=?`, f.sw.SessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
}

func TestFailedMutationAndOutboxCommitAtomically(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	var cdcBefore int
	if err := f.db.QueryRow(`SELECT count(*) FROM change_log WHERE session_id=?`, f.sw.SessionID).Scan(&cdcBefore); err != nil {
		t.Fatalf("count CDC before mutation: %v", err)
	}
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f))
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentEnrolled {
		t.Fatalf("ApplyAgentSwitchMutation = %+v, %v", result, err)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_outbox"); got != 1 {
		t.Fatalf("outbox rows = %d, want 1", got)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_receipts"); got != 1 {
		t.Fatalf("receipt rows = %d, want 1", got)
	}
	var canonical []byte
	if err := f.db.QueryRow(`SELECT canonical_event_json FROM agent_switch_failure_outbox`).Scan(&canonical); err != nil {
		t.Fatalf("read frozen canonical event: %v", err)
	}
	for _, field := range [][]byte{[]byte(`"release":"1.2.3"`), []byte(`"environment":"development"`), []byte(`"channel":"preview"`), []byte(`"os":"linux"`)} {
		if !bytes.Contains(canonical, field) {
			t.Fatalf("canonical event does not contain configured metadata %s: %s", field, canonical)
		}
	}
	var cdcAfter int
	if err := f.db.QueryRow(`SELECT count(*) FROM change_log WHERE session_id=?`, f.sw.SessionID).Scan(&cdcAfter); err != nil {
		t.Fatalf("count CDC after mutation: %v", err)
	}
	if cdcAfter != cdcBefore+1 {
		t.Fatalf("failure store emitted %d CDC rows, want exactly one trigger-owned row", cdcAfter-cdcBefore)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchFailed || stored.FailurePoint != domain.AgentSwitchFailureWorkerStartRefused {
		t.Fatalf("stored switch = %+v, ok=%v err=%v", stored, ok, err)
	}
}

func TestAgentSwitchFailurePolicyDisabledAndStaleGenerationCommitCoreOnly(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enablePolicy  bool
		authorization domain.AgentSwitchReportingAuthorization
		want          domain.AgentSwitchEnrollmentStatus
	}{
		{name: "disabled", authorization: domain.AgentSwitchReportingAuthorization{}, want: domain.AgentSwitchEnrollmentDisabled},
		{name: "stale generation", enablePolicy: true, authorization: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "stale", DestinationFingerprint: "destination-1"}, want: domain.AgentSwitchEnrollmentStaleGeneration},
		{name: "stale destination", enablePolicy: true, authorization: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation-1", DestinationFingerprint: "destination-2"}, want: domain.AgentSwitchEnrollmentStaleGeneration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := openAgentSwitchFailureFixture(t)
			if tc.enablePolicy {
				f.enablePolicy(t)
			}
			mutation := failedMutation(f)
			mutation.Authorization = tc.authorization
			result, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation)
			if err != nil || !result.CoreChanged || result.Enrollment != tc.want {
				t.Fatalf("result = %+v, err=%v", result, err)
			}
			if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
				t.Fatal("disabled or stale policy enrolled telemetry")
			}
		})
	}
}

func TestMissingEventMetadataIsTelemetryLocalAndCoreCommits(t *testing.T) {
	f := openAgentSwitchFailureFixtureWithMetadata(t, false)
	invalid := testAgentSwitchFailureEventMetadata()
	invalid.Release = "0.0"
	if err := f.store.ConfigureAgentSwitchFailureEventMetadata(context.Background(), invalid); err == nil {
		t.Fatal("invalid event metadata was accepted")
	}
	f.enablePolicy(t)
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f))
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("missing-metadata result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchFailed || stored.FailurePoint != domain.AgentSwitchFailureWorkerStartRefused {
		t.Fatalf("core mutation after missing metadata = %+v, ok=%v err=%v", stored, ok, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("missing metadata left telemetry rows")
	}
}

func TestMismatchedObservabilityCannotVetoCoreFailureMutation(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	mutation := failedMutation(f)
	mutation.Fault.FromHarness = domain.HarnessCodex
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation)
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("mismatched observability result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchFailed || stored.ErrorCode != mutation.Record.ErrorCode || stored.FailurePoint != mutation.Record.FailurePoint {
		t.Fatalf("core failure after mismatched observability = %+v, ok=%v err=%v", stored, ok, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("mismatched observability left telemetry rows")
	}
}

func TestMalformedObservabilityCannotVetoCoreRecoveryMarkerMutation(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.db.Exec(`UPDATE agent_switches SET state='stopping_source',updated_at=? WHERE id=?`, f.now.Add(time.Second), f.sw.ID); err != nil {
		t.Fatalf("seed recovery marker state: %v", err)
	}
	mutation := recoveryMarkerMutation(f)
	mutation.Fault.FailurePoint = domain.AgentSwitchFailurePoint("not-in-compiled-taxonomy")
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation)
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("malformed observability result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchStoppingSource || stored.ErrorCode != mutation.Record.ErrorCode || stored.FailurePoint != mutation.Record.FailurePoint {
		t.Fatalf("core recovery marker after malformed observability = %+v, ok=%v err=%v", stored, ok, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("malformed observability left telemetry rows")
	}
}

func TestReceiptResolutionFailureRollsBackTelemetryOnly(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	if _, err := f.db.Exec(`
INSERT INTO agent_switch_failure_receipts (
 dedupe_key,switch_id,report_kind,durable_state_fingerprint,recorded_at,retain_until
) VALUES ('unresolved-before-progress',?,'recovery_required','v1:old',?,NULL)`, f.sw.ID, f.now); err != nil {
		t.Fatalf("seed unresolved receipt: %v", err)
	}
	if _, err := f.db.Exec(`
CREATE TRIGGER abort_agent_switch_failure_receipt_resolution
BEFORE UPDATE ON agent_switch_failure_receipts
BEGIN SELECT RAISE(ABORT, 'injected receipt resolution failure'); END;`); err != nil {
		t.Fatalf("install receipt resolution trigger: %v", err)
	}
	rec := f.sw
	rec.State = domain.AgentSwitchStoppingSource
	rec.UpdatedAt = f.now.Add(time.Second)
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), ports.AgentSwitchMutation{
		Record: rec, ExpectedState: domain.AgentSwitchPreparingHandoff,
		ExpectedSourceGenerationID: rec.SourceGenerationID,
	})
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("ordinary progress result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchStoppingSource {
		t.Fatalf("core progress did not survive receipt failure: %+v, ok=%v err=%v", stored, ok, err)
	}
	var retainUntil sql.NullTime
	if err := f.db.QueryRow(`SELECT retain_until FROM agent_switch_failure_receipts WHERE dedupe_key='unresolved-before-progress'`).Scan(&retainUntil); err != nil || retainUntil.Valid {
		t.Fatalf("failed resolution changed receipt: %+v err=%v", retainUntil, err)
	}
}

func TestZeroRowCASAndRepeatedMarkerDoNotEnroll(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	mutation := failedMutation(f)
	mutation.ExpectedSourceGenerationID = "stale-source"
	mutation.Record.SourceGenerationID = "stale-source"
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation)
	if err != nil || result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentDeduped {
		t.Fatalf("stale result = %+v, err=%v", result, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 {
		t.Fatal("zero-row CAS enrolled a payload")
	}

	if _, err := f.db.Exec(`UPDATE agent_switches SET state='stopping_source',updated_at=? WHERE id=?`, f.now.Add(time.Second), f.sw.ID); err != nil {
		t.Fatalf("seed stopping-source switch: %v", err)
	}
	first := recoveryMarkerMutation(f)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), first); err != nil {
		t.Fatalf("first marker: %v", err)
	}
	result, err = f.store.ApplyAgentSwitchMutation(context.Background(), first)
	if err != nil || result.CoreChanged || countFailureRows(t, f.db, "agent_switch_failure_outbox") != 1 {
		t.Fatalf("repeated mutation = %+v, err=%v", result, err)
	}
}

func recoveryMarkerMutation(f agentSwitchFailureFixture) ports.AgentSwitchMutation {
	rec := f.sw
	rec.State = domain.AgentSwitchStoppingSource
	rec.ErrorCode = domain.AgentSwitchErrorSourceStopUnconfirmed
	rec.FailurePoint = domain.AgentSwitchFailureSourceRuntimeProbe
	rec.UpdatedAt = f.now.Add(2 * time.Second)
	fault := domain.AgentSwitchFault{
		ReportKind:   domain.AgentSwitchReportRecoveryRequired,
		FailurePoint: rec.FailurePoint, ClassifierCallsite: domain.AgentSwitchClassifierExecuteTUI,
		Phase: rec.State, ErrorCode: rec.ErrorCode, FaultCode: domain.AgentSwitchFaultNotApplicable,
		Execution: domain.AgentSwitchExecutionLive, Mode: domain.SessionModeTUI,
		FromHarness: rec.FromHarness, TargetHarness: rec.TargetHarness,
		TargetStartMode: domain.AgentSwitchTargetStartReportedPending,
		RuntimeBackend:  domain.AgentSwitchRuntimeTMUX, CallOutcome: domain.AgentSwitchCallEffectUnknown,
		Ownership: domain.AgentSwitchOwnershipAmbiguous, Compensation: domain.AgentSwitchCompensationUncertain,
		UserImpact:          domain.AgentSwitchUserImpactGateRetained,
		SourceStopConfirmed: domain.AgentSwitchTriFalse, TargetOwnerCommitted: domain.AgentSwitchTriFalse,
		GateRetained: domain.AgentSwitchTriTrue, OccurredAt: rec.UpdatedAt,
		Frames: []domain.AgentSwitchStackFrame{{Package: "session_manager", Function: "Manager.probeSource", Filename: "backend/internal/session_manager/agent_switching.go", Line: 1}},
	}
	return ports.AgentSwitchMutation{
		Record: rec, ExpectedState: domain.AgentSwitchStoppingSource,
		ExpectedSourceGenerationID: "source-1", ExpectedTargetGenerationID: "",
		Fault: &fault, Authorization: f.authorization(),
	}
}

func TestAgentSwitchFailureOutboxSavepointAbortsTelemetryOnly(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.db.Exec(`
CREATE TRIGGER abort_agent_switch_failure_outbox
BEFORE INSERT ON agent_switch_failure_outbox
BEGIN SELECT RAISE(ABORT, 'injected outbox failure'); END;
`); err != nil {
		t.Fatalf("install abort trigger: %v", err)
	}
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f))
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchFailed || stored.FailurePoint == "" {
		t.Fatalf("core mutation did not survive telemetry abort: %+v ok=%v err=%v", stored, ok, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("telemetry savepoint left partial rows")
	}
}

func TestAgentSwitchFailureSerializationFailureIsTelemetryLocal(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	mutation := failedMutation(f)
	frame := domain.AgentSwitchStackFrame{
		Package: "session_manager", Function: "Manager.workerStart",
		Filename: "backend/internal/session_manager/agent_switching.go", Line: 1,
	}
	mutation.Fault.Frames = make([]domain.AgentSwitchStackFrame, 500)
	for i := range mutation.Fault.Frames {
		mutation.Fault.Frames[i] = frame
	}
	result, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation)
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("oversized serialization result = %+v, err=%v", result, err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchFailed || stored.FailurePoint == "" {
		t.Fatalf("core mutation did not survive serialization failure: %+v ok=%v err=%v", stored, ok, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("serialization failure left partial telemetry rows")
	}
}

func TestAgentSwitchFailureCoreAbortRollsBackEverything(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.db.Exec(`
CREATE TRIGGER abort_agent_switch_core BEFORE UPDATE ON agent_switches
BEGIN SELECT RAISE(ABORT, 'injected core failure'); END;
`); err != nil {
		t.Fatalf("install abort trigger: %v", err)
	}
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err == nil {
		t.Fatal("core abort unexpectedly succeeded")
	}
	if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 || countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
		t.Fatal("core rollback left telemetry rows")
	}
}

func TestAcknowledgementAndTimeoutRaceOrders(t *testing.T) {
	for _, ackFirst := range []bool{true, false} {
		name := "timeout_first"
		if ackFirst {
			name = "acknowledgement_first"
		}
		t.Run(name, func(t *testing.T) {
			f := openAgentSwitchFailureFixture(t)
			f.enablePolicy(t)
			seedDeliveringSwitch(t, f)
			failed := deliveringFailureMutation(f)
			if ackFirst {
				changed, err := f.store.AcknowledgeAgentSwitchTarget(context.Background(), f.sw.ID, f.sw.SessionID, "target-1", f.now.Add(5*time.Second))
				if err != nil || !changed {
					t.Fatalf("acknowledge: changed=%v err=%v", changed, err)
				}
				result, err := f.store.FailAgentSwitchIfUnacknowledgedWithFault(context.Background(), failed)
				if err != nil || result.CoreChanged || countFailureRows(t, f.db, "agent_switch_failure_outbox") != 0 {
					t.Fatalf("ack-first failure result = %+v, err=%v", result, err)
				}
				return
			}
			result, err := f.store.FailAgentSwitchIfUnacknowledgedWithFault(context.Background(), failed)
			if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentEnrolled {
				t.Fatalf("timeout-first result = %+v, err=%v", result, err)
			}
			changed, err := f.store.AcknowledgeAgentSwitchTarget(context.Background(), f.sw.ID, f.sw.SessionID, "target-1", f.now.Add(6*time.Second))
			if err != nil || changed || countFailureRows(t, f.db, "agent_switch_failure_outbox") != 1 {
				t.Fatalf("late acknowledgement: changed=%v err=%v", changed, err)
			}
		})
	}
}

func seedDeliveringSwitch(t *testing.T, f agentSwitchFailureFixture) {
	t.Helper()
	if _, err := f.db.Exec(`
UPDATE agent_switches
SET state='delivering_context', target_start_mode='fresh', target_generation_id='target-1',
    target_runtime_handle_id='target-handle', updated_at=?
WHERE id=?`, f.now.Add(time.Second), f.sw.ID); err != nil {
		t.Fatalf("seed delivering switch: %v", err)
	}
}

func deliveringFailureMutation(f agentSwitchFailureFixture) ports.AgentSwitchMutation {
	rec := f.sw
	rec.State = domain.AgentSwitchFailed
	rec.TargetStartMode = domain.AgentSwitchTargetStartFresh
	rec.TargetGenerationID = "target-1"
	rec.TargetRuntimeHandleID = "target-handle"
	rec.ErrorCode = domain.AgentSwitchErrorDeliveryUnconfirmed
	rec.FailurePoint = domain.AgentSwitchFailureTUITargetAckCommit
	rec.UpdatedAt = f.now.Add(5 * time.Second)
	fault := domain.AgentSwitchFault{
		ReportKind: domain.AgentSwitchReportTerminalFailure, FailurePoint: rec.FailurePoint,
		ClassifierCallsite: domain.AgentSwitchClassifierExecuteTUI, Phase: domain.AgentSwitchDelivering,
		ErrorCode: rec.ErrorCode, FaultCode: domain.AgentSwitchFaultNotApplicable,
		Execution: domain.AgentSwitchExecutionLive, Mode: domain.SessionModeTUI,
		FromHarness: rec.FromHarness, TargetHarness: rec.TargetHarness,
		TargetStartMode: rec.TargetStartMode, RuntimeBackend: domain.AgentSwitchRuntimeTMUX,
		CallOutcome: domain.AgentSwitchCallTimedOut, Ownership: domain.AgentSwitchOwnershipTarget,
		Compensation: domain.AgentSwitchCompensationNotNeeded, UserImpact: domain.AgentSwitchUserImpactDeliveryUnknown,
		SourceStopConfirmed: domain.AgentSwitchTriTrue, TargetOwnerCommitted: domain.AgentSwitchTriTrue,
		GateRetained: domain.AgentSwitchTriFalse, OccurredAt: rec.UpdatedAt,
		Frames: []domain.AgentSwitchStackFrame{{Package: "session_manager", Function: "Manager.ack", Filename: "backend/internal/session_manager/agent_switching.go", Line: 1}},
	}
	return ports.AgentSwitchMutation{
		Record: rec, ExpectedState: domain.AgentSwitchDelivering,
		ExpectedSourceGenerationID: "source-1", ExpectedTargetGenerationID: "target-1",
		Fault: &fault, Authorization: f.authorization(),
	}
}

func TestStandaloneEnqueueDeletionOrdersAndReceiptCascade(t *testing.T) {
	for _, deleteFirst := range []bool{true, false} {
		name := "enqueue_first"
		if deleteFirst {
			name = "deletion_first"
		}
		t.Run(name, func(t *testing.T) {
			f := openAgentSwitchFailureFixture(t)
			f.enablePolicy(t)
			mutation := failedMutation(f)
			if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), mutation); err != nil {
				t.Fatalf("terminalize switch: %v", err)
			}
			stored, _, _ := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
			op := maintenanceFault(f, stored)
			if deleteFirst {
				deleteFailureFixtureSession(t, f)
				result, err := f.store.EnqueueAgentSwitchOperationalFault(context.Background(), op)
				if err != nil || result.CoreChanged || countFailureRows(t, f.db, "agent_switch_failure_outbox") != 1 {
					t.Fatalf("post-delete enqueue = %+v, err=%v", result, err)
				}
				return
			}
			result, err := f.store.EnqueueAgentSwitchOperationalFault(context.Background(), op)
			if err != nil || !result.CoreChanged || countFailureRows(t, f.db, "agent_switch_failure_outbox") != 2 {
				t.Fatalf("standalone enqueue = %+v, err=%v", result, err)
			}
			deleteFailureFixtureSession(t, f)
			if countFailureRows(t, f.db, "agent_switch_failure_receipts") != 0 {
				t.Fatal("switch receipt did not cascade")
			}
			if countFailureRows(t, f.db, "agent_switch_failure_outbox") != 2 {
				t.Fatal("consented payload did not survive product-data deletion")
			}
		})
	}
}

func TestStandaloneEnqueueRejectsFaultThatDoesNotMatchDurableSwitch(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("terminalize switch: %v", err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok {
		t.Fatalf("read terminal switch: ok=%v err=%v", ok, err)
	}
	op := maintenanceFault(f, stored)
	op.Fault.FromHarness = domain.HarnessCodex
	if _, err := f.store.EnqueueAgentSwitchOperationalFault(context.Background(), op); err == nil {
		t.Fatal("standalone enqueue accepted a mismatched harness direction")
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_outbox"); got != 1 {
		t.Fatalf("mismatched standalone fault changed outbox rows to %d", got)
	}
}

func maintenanceFault(f agentSwitchFailureFixture, sw domain.AgentSwitch) ports.AgentSwitchOperationalFault {
	fault := terminalFault(f.now.Add(2 * time.Second))
	fault.ReportKind = domain.AgentSwitchReportMaintenanceFailure
	fault.FailurePoint = domain.AgentSwitchFailureTerminalArtifactCleanup
	fault.ClassifierCallsite = domain.AgentSwitchClassifierTerminalMaintenance
	fault.Phase = domain.AgentSwitchFailed
	fault.ErrorCode = sw.ErrorCode
	fault.FaultCode = domain.AgentSwitchFaultTerminalCleanupFailed
	fault.CallOutcome = domain.AgentSwitchCallCleanupFailed
	return ports.AgentSwitchOperationalFault{
		SwitchID: sw.ID, ExpectedState: sw.State, ExpectedErrorCode: sw.ErrorCode,
		ExpectedFailurePoint: sw.FailurePoint, ExpectedUpdatedAt: sw.UpdatedAt,
		DaemonRunID: "daemon-run-1", Fault: fault, Authorization: f.authorization(),
	}
}

func TestResolveReceiptsExpiryPurgeAndDestinationQuarantine(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.db.Exec(`UPDATE agent_switches SET state='stopping_source',updated_at=? WHERE id=?`, f.now.Add(time.Second), f.sw.ID); err != nil {
		t.Fatalf("seed recovery marker state: %v", err)
	}
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), recoveryMarkerMutation(f)); err != nil {
		t.Fatalf("enroll unresolved marker: %v", err)
	}
	var unresolved sql.NullTime
	var fingerprint string
	if err := f.db.QueryRow(`SELECT retain_until,durable_state_fingerprint FROM agent_switch_failure_receipts LIMIT 1`).Scan(&unresolved, &fingerprint); err != nil || unresolved.Valid {
		t.Fatalf("unresolved receipt retention = %+v, err=%v", unresolved, err)
	}
	wantFingerprint := sha256.Sum256([]byte("stopping_source|source_stop_unconfirmed|source_runtime_probe"))
	if want := fmt.Sprintf("v1:%x", wantFingerprint); fingerprint != want {
		t.Fatalf("durable fingerprint = %q, want %q", fingerprint, want)
	}
	if _, err := f.store.ResolveAgentSwitchFailureReceipts(context.Background(), ports.AgentSwitchFailureReceiptResolution{
		SwitchID: f.sw.ID, DurableStateFingerprint: "new-fingerprint", ResolvedAt: f.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("resolve receipts: %v", err)
	}
	var retainUntil sql.NullTime
	if err := f.db.QueryRow(`SELECT retain_until FROM agent_switch_failure_receipts LIMIT 1`).Scan(&retainUntil); err != nil || !retainUntil.Valid {
		t.Fatalf("resolved retain_until = %+v, err=%v", retainUntil, err)
	}

	if _, err := f.db.Exec(`UPDATE agent_switch_failure_outbox SET delivered_at=?, discarded_at=NULL`, f.now.Add(time.Hour)); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if purged, err := f.store.PurgeAgentSwitchFailurePayloads(context.Background()); err != nil || purged != 1 {
		t.Fatalf("purge delivered payload = %d, %v", purged, err)
	}
	if countFailureRows(t, f.db, "agent_switch_failure_receipts") != 1 {
		t.Fatal("opt-out purge removed payload-free receipt")
	}

	// Re-enroll an independent daemon payload and prove destination rotation
	// quarantines rather than migrates it to the new project.
	daemonFault := terminalFault(f.now.Add(2 * time.Hour))
	daemonFault.ReportKind = domain.AgentSwitchReportDaemonLifecycleFailure
	daemonFault.FailurePoint = domain.AgentSwitchFailureShutdownWorkerTimeout
	daemonFault.ClassifierCallsite = domain.AgentSwitchClassifierDaemonShutdown
	daemonFault.Phase = domain.AgentSwitchStateNotApplicable
	daemonFault.ErrorCode = domain.AgentSwitchErrorNotApplicable
	daemonFault.FaultCode = domain.AgentSwitchFaultShutdownWorkersTimedOut
	daemonFault.Execution = domain.AgentSwitchExecutionDaemonShutdown
	daemonFault.Mode = domain.SessionModeNotApplicable
	daemonFault.FromHarness = domain.HarnessNotApplicable
	daemonFault.TargetHarness = domain.HarnessNotApplicable
	daemonFault.TargetStartMode = domain.AgentSwitchTargetStartNotApplicable
	daemonFault.RuntimeBackend = domain.AgentSwitchRuntimeNotApplicable
	daemonFault.Ownership = domain.AgentSwitchOwnershipNotApplicable
	daemonFault.Compensation = domain.AgentSwitchCompensationNotApplicable
	daemonFault.UserImpact = domain.AgentSwitchUserImpactNotApplicable
	daemonFault.SourceStopConfirmed = domain.AgentSwitchTriNotApplicable
	daemonFault.TargetOwnerCommitted = domain.AgentSwitchTriNotApplicable
	daemonFault.GateRetained = domain.AgentSwitchTriNotApplicable
	daemonFault.Frames = []domain.AgentSwitchStackFrame{{
		Package: "daemon", Function: "Daemon.waitAgentSwitchWorkers",
		Filename: "backend/internal/daemon/daemon.go", Line: 1,
	}}
	if _, err := f.store.EnqueueAgentSwitchDaemonFault(context.Background(), ports.AgentSwitchDaemonFault{
		DaemonRunID: "daemon-run-2", Fault: daemonFault, Authorization: f.authorization(),
	}); err != nil {
		t.Fatalf("enqueue daemon fault: %v", err)
	}
	rotatedAuthorization := domain.AgentSwitchReportingAuthorization{
		Enabled: true, ConsentGeneration: "generation-2", DestinationFingerprint: "destination-2",
	}
	if err := f.store.ApplyAgentSwitchFailurePolicy(context.Background(), ports.AgentSwitchFailurePolicy{
		Authorization: rotatedAuthorization, UpdatedAt: f.now.Add(2*time.Hour + time.Second),
	}); err != nil {
		t.Fatalf("rotate failure policy destination: %v", err)
	}
	claim, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: rotatedAuthorization,
		DeliveryEpoch: 3, LeaseToken: "lease-1", Now: f.now.Add(3 * time.Hour), LeaseExpiresAt: f.now.Add(3*time.Hour + 30*time.Second),
	})
	if err != nil || ok || claim.ID != "" {
		t.Fatalf("mismatched destination claim = %+v, ok=%v err=%v", claim, ok, err)
	}
	var discarded sql.NullTime
	if err := f.db.QueryRow(`SELECT discarded_at FROM agent_switch_failure_outbox LIMIT 1`).Scan(&discarded); err != nil || !discarded.Valid {
		t.Fatalf("rotated payload was not quarantined: %+v err=%v", discarded, err)
	}

	if _, err := f.db.Exec(`UPDATE agent_switch_failure_outbox SET expires_at=?, discarded_at=NULL`, f.now.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("set exact TTL: %v", err)
	}
	if expired, err := f.store.ExpireAgentSwitchFailurePayloads(context.Background(), f.now.Add(7*24*time.Hour)); err != nil || expired != 1 {
		t.Fatalf("expire at seven days = %d, %v", expired, err)
	}
}

func TestOptOutPurgesEveryPayloadStatus(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.db.Exec(`
INSERT INTO agent_switch_failure_receipts (
 dedupe_key,switch_id,report_kind,durable_state_fingerprint,recorded_at,retain_until
) VALUES ('opt-out-receipt',NULL,'daemon_lifecycle_failure','daemon|run',?,?)`,
		f.now, f.now.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("seed payload-free receipt: %v", err)
	}
	statuses := []string{"pending", "leased", "delivered", "discarded"}
	for i, status := range statuses {
		if _, err := f.db.Exec(`
INSERT INTO agent_switch_failure_outbox (
 id,schema_version,envelope_encoding_version,dedupe_key,destination_fingerprint,switch_id,
 report_kind,scope,failure_point,classifier_callsite,phase,error_code,fault_code,execution,
 execution_attempt_id,mode,from_harness,target_harness,target_start_mode,runtime_backend,
 call_outcome,ownership,compensation,user_impact,source_stop_confirmed,target_owner_committed,
 gate_retained,occurred_at,sanitized_stack,stack_fingerprint,canonical_event_json,expires_at,available_at
) VALUES (?,?,?,?,? ,NULL,'daemon_lifecycle_failure','daemon','shutdown_worker_timeout',
 'daemon.wait_agent_switch_workers','not_applicable','not_applicable','shutdown_workers_timed_out',
 'daemon_shutdown','','not_applicable','not_applicable','not_applicable','not_applicable','not_applicable',
 'timed_out','not_applicable','not_applicable','not_applicable','not_applicable','not_applicable',
 'not_applicable',?,X'', '', X'7b7d',?,?)`,
			"row-"+status, 1, 1, "dedupe-"+status, "destination-1",
			f.now.Add(time.Duration(i)*time.Second), f.now.Add(8*24*time.Hour), f.now); err != nil {
			t.Fatalf("seed %s payload: %v", status, err)
		}
		switch status {
		case "leased":
			_, _ = f.db.Exec(`UPDATE agent_switch_failure_outbox SET lease_token='lease', lease_expires_at=? WHERE id=?`, f.now.Add(time.Minute), "row-"+status)
		case "delivered":
			_, _ = f.db.Exec(`UPDATE agent_switch_failure_outbox SET delivered_at=? WHERE id=?`, f.now, "row-"+status)
		case "discarded":
			_, _ = f.db.Exec(`UPDATE agent_switch_failure_outbox SET discarded_at=? WHERE id=?`, f.now, "row-"+status)
		}
	}
	if err := f.store.ApplyAgentSwitchFailurePolicy(context.Background(), ports.AgentSwitchFailurePolicy{
		Authorization: domain.AgentSwitchReportingAuthorization{
			Enabled: false, ConsentGeneration: "generation-2", DestinationFingerprint: "destination-1",
		},
		UpdatedAt: f.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("apply opt-out policy: %v", err)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_outbox"); got != 0 {
		t.Fatalf("payload rows after opt-out = %d, want 0", got)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_receipts"); got != 1 {
		t.Fatalf("payload-free receipts after opt-out = %d, want 1", got)
	}
	var enabled bool
	var generation, destination string
	if err := f.db.QueryRow(`SELECT enabled,consent_generation,destination_fingerprint FROM agent_switch_failure_policy WHERE singleton=1`).Scan(&enabled, &generation, &destination); err != nil {
		t.Fatalf("read opt-out policy mirror: %v", err)
	}
	if enabled || generation != "generation-2" || destination != "destination-1" {
		t.Fatalf("opt-out policy mirror = enabled=%v generation=%q destination=%q", enabled, generation, destination)
	}
}

func TestOptOutPolicyAndPayloadPurgeRollbackTogether(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("seed failure payload: %v", err)
	}
	if _, err := f.db.Exec(`
CREATE TRIGGER reject_agent_switch_failure_opt_out
BEFORE DELETE ON agent_switch_failure_outbox
BEGIN
 SELECT RAISE(ABORT, 'reject opt-out purge');
END`); err != nil {
		t.Fatalf("install opt-out rollback trigger: %v", err)
	}

	err := f.store.ApplyAgentSwitchFailurePolicy(context.Background(), ports.AgentSwitchFailurePolicy{
		Authorization: domain.AgentSwitchReportingAuthorization{
			Enabled: false, ConsentGeneration: "generation-2", DestinationFingerprint: "destination-1",
		},
		UpdatedAt: f.now.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("opt-out unexpectedly committed after purge failure")
	}
	var enabled bool
	var generation, destination string
	if err := f.db.QueryRow(`SELECT enabled,consent_generation,destination_fingerprint FROM agent_switch_failure_policy WHERE singleton=1`).Scan(&enabled, &generation, &destination); err != nil {
		t.Fatalf("read rolled-back policy mirror: %v", err)
	}
	if !enabled || generation != "generation-1" || destination != "destination-1" {
		t.Fatalf("rolled-back policy mirror = enabled=%v generation=%q destination=%q", enabled, generation, destination)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_outbox"); got != 1 {
		t.Fatalf("payload rows after rollback = %d, want 1", got)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_receipts"); got != 1 {
		t.Fatalf("receipt rows after rollback = %d, want 1", got)
	}
}

func TestFinalAttemptRequiresLeaseGenerationEpochDestinationThrottleAndTTL(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	claim, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 7, LeaseToken: "lease-7",
		Now: f.now.Add(time.Minute), LeaseExpiresAt: f.now.Add(time.Minute + 30*time.Second),
	})
	if err != nil || !ok {
		t.Fatalf("claim = %+v, ok=%v err=%v", claim, ok, err)
	}
	for _, mutate := range []func(*ports.AgentSwitchFailureAttempt){
		func(a *ports.AgentSwitchFailureAttempt) { a.LeaseToken = "wrong" },
		func(a *ports.AgentSwitchFailureAttempt) { a.ConsentGeneration = "wrong" },
		func(a *ports.AgentSwitchFailureAttempt) { a.DeliveryEpoch++ },
		func(a *ports.AgentSwitchFailureAttempt) { a.DestinationFingerprint = "wrong" },
		func(a *ports.AgentSwitchFailureAttempt) { a.Now = f.now.Add(time.Minute + 30*time.Second) },
		func(a *ports.AgentSwitchFailureAttempt) { a.Now = claim.ExpiresAt },
	} {
		attempt := ports.AgentSwitchFailureAttempt{
			ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
			DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
			Now: f.now.Add(time.Minute),
		}
		mutate(&attempt)
		if changed, err := f.store.BeginAgentSwitchFailureAttempt(context.Background(), attempt); err != nil || changed {
			t.Fatalf("invalid final attempt changed=%v err=%v input=%+v", changed, err, attempt)
		}
	}
	if _, err := f.db.Exec(`
INSERT INTO agent_switch_failure_delivery_state(destination_fingerprint,error_not_before,all_not_before)
VALUES ('destination-1',NULL,?)
ON CONFLICT(destination_fingerprint) DO UPDATE SET all_not_before=excluded.all_not_before`, f.now.Add(2*time.Hour)); err != nil {
		t.Fatalf("seed throttle: %v", err)
	}
	attempt := ports.AgentSwitchFailureAttempt{
		ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
		Now: f.now.Add(time.Minute),
	}
	if changed, err := f.store.BeginAgentSwitchFailureAttempt(context.Background(), attempt); err != nil || changed {
		t.Fatalf("throttled final attempt changed=%v err=%v", changed, err)
	}
	if _, err := f.db.Exec(`UPDATE agent_switch_failure_delivery_state SET error_not_before=NULL,all_not_before=NULL WHERE destination_fingerprint='destination-1'`); err != nil {
		t.Fatalf("clear throttle: %v", err)
	}
	if changed, err := f.store.BeginAgentSwitchFailureAttempt(context.Background(), attempt); err != nil || !changed {
		t.Fatalf("fully fenced final attempt changed=%v err=%v", changed, err)
	}
}

func TestSettlementCASPrecedesAndAtomicallyCommitsThrottle(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	claim, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 9, LeaseToken: "lease-9",
		Now: f.now.Add(time.Minute), LeaseExpiresAt: f.now.Add(2 * time.Minute),
	})
	if err != nil || !ok {
		t.Fatalf("claim = %+v, ok=%v err=%v", claim, ok, err)
	}
	settlement := ports.AgentSwitchFailureSettlement{
		ID: claim.ID, LeaseToken: "stale-lease", ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
		SettledAt: f.now.Add(90 * time.Second),
		Result: ports.DeliveryResult{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone,
			RetryNotBefore: f.now.Add(time.Hour), ThrottleScope: ports.DeliveryThrottleAll},
	}
	if changed, err := f.store.SettleAgentSwitchFailureDelivery(context.Background(), settlement); err != nil || changed {
		t.Fatalf("stale settlement changed=%v err=%v", changed, err)
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_delivery_state"); got != 0 {
		t.Fatalf("stale settlement wrote %d throttle rows", got)
	}
	settlement.LeaseToken = claim.LeaseToken
	if changed, err := f.store.SettleAgentSwitchFailureDelivery(context.Background(), settlement); err != nil || !changed {
		t.Fatalf("valid accepted settlement changed=%v err=%v", changed, err)
	}
	var delivered, allNotBefore sql.NullTime
	if err := f.db.QueryRow(`
SELECT o.delivered_at,d.all_not_before
FROM agent_switch_failure_outbox o
JOIN agent_switch_failure_delivery_state d ON d.destination_fingerprint=o.destination_fingerprint
WHERE o.id=?`, claim.ID).Scan(&delivered, &allNotBefore); err != nil || !delivered.Valid || !allNotBefore.Valid {
		t.Fatalf("accepted settlement/throttle not atomic: delivered=%+v throttle=%+v err=%v", delivered, allNotBefore, err)
	}
}

func TestSettlementThrottleFailureRollsBackOutboxCAS(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	claim, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 10, LeaseToken: "lease-10",
		Now: f.now.Add(time.Minute), LeaseExpiresAt: f.now.Add(2 * time.Minute),
	})
	if err != nil || !ok {
		t.Fatalf("claim = %+v, ok=%v err=%v", claim, ok, err)
	}
	if _, err := f.db.Exec(`
CREATE TRIGGER abort_agent_switch_failure_throttle
BEFORE INSERT ON agent_switch_failure_delivery_state
BEGIN SELECT RAISE(ABORT, 'injected throttle failure'); END;`); err != nil {
		t.Fatalf("install throttle abort trigger: %v", err)
	}
	changed, err := f.store.SettleAgentSwitchFailureDelivery(context.Background(), ports.AgentSwitchFailureSettlement{
		ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
		SettledAt: f.now.Add(90 * time.Second),
		Result: ports.DeliveryResult{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone,
			RetryNotBefore: f.now.Add(time.Hour), ThrottleScope: ports.DeliveryThrottleAll},
	})
	if err == nil || changed {
		t.Fatalf("throttle failure settlement changed=%v err=%v", changed, err)
	}
	var delivered sql.NullTime
	var lease sql.NullString
	if err := f.db.QueryRow(`SELECT delivered_at,lease_token FROM agent_switch_failure_outbox WHERE id=?`, claim.ID).Scan(&delivered, &lease); err != nil || delivered.Valid || !lease.Valid || lease.String != claim.LeaseToken {
		t.Fatalf("outbox CAS survived throttle rollback: delivered=%+v lease=%+v err=%v", delivered, lease, err)
	}
}

func TestTransientAllScopeThrottleUsesLocalRetryCapsAtTTLAndBlocksOtherRows(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	f.enablePolicy(t)
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err != nil {
		t.Fatalf("enroll terminal payload: %v", err)
	}
	stored, ok, err := f.store.GetAgentSwitch(context.Background(), f.sw.ID)
	if err != nil || !ok {
		t.Fatalf("read terminal switch: ok=%v err=%v", ok, err)
	}
	if result, err := f.store.EnqueueAgentSwitchOperationalFault(context.Background(), maintenanceFault(f, stored)); err != nil || !result.CoreChanged {
		t.Fatalf("enroll second payload: result=%+v err=%v", result, err)
	}
	claimedAt := f.now.Add(time.Minute)
	claim, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 11, LeaseToken: "lease-11",
		Now: claimedAt, LeaseExpiresAt: claimedAt.Add(30 * time.Second),
	})
	if err != nil || !ok {
		t.Fatalf("claim first payload = %+v, ok=%v err=%v", claim, ok, err)
	}
	settledAt := claimedAt.Add(10 * time.Second)
	changed, err := f.store.SettleAgentSwitchFailureDelivery(context.Background(), ports.AgentSwitchFailureSettlement{
		ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
		SettledAt: settledAt, NextAvailableAt: claim.ExpiresAt.Add(time.Hour),
		Result: ports.DeliveryResult{
			Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryErrorRateLimited,
			ThrottleScope: ports.DeliveryThrottleAll,
		},
	})
	if err != nil || !changed {
		t.Fatalf("settle headerless throttle: changed=%v err=%v", changed, err)
	}
	var availableAt, allNotBefore time.Time
	if err := f.db.QueryRow(`
SELECT o.available_at,d.all_not_before
FROM agent_switch_failure_outbox o
JOIN agent_switch_failure_delivery_state d ON d.destination_fingerprint=o.destination_fingerprint
WHERE o.id=?`, claim.ID).Scan(&availableAt, &allNotBefore); err != nil {
		t.Fatalf("read persisted throttle: %v", err)
	}
	if !availableAt.Equal(claim.ExpiresAt) || !allNotBefore.Equal(claim.ExpiresAt) {
		t.Fatalf("persisted deadlines = available=%s all=%s, want TTL %s", availableAt, allNotBefore, claim.ExpiresAt)
	}
	blockedAt := claim.ExpiresAt.Add(-time.Second)
	if blocked, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 11, LeaseToken: "lease-blocked",
		Now: blockedAt, LeaseExpiresAt: blockedAt.Add(30 * time.Second),
	}); err != nil || ok || blocked.ID != "" {
		t.Fatalf("all-scope throttle allowed another claim = %+v, ok=%v err=%v", blocked, ok, err)
	}
	next, ok, err := f.store.ClaimAgentSwitchFailure(context.Background(), ports.AgentSwitchFailureClaimRequest{
		Authorization: f.authorization(), DeliveryEpoch: 11, LeaseToken: "lease-next",
		Now: claim.ExpiresAt, LeaseExpiresAt: claim.ExpiresAt.Add(30 * time.Second),
	})
	if err != nil || !ok || next.ID == claim.ID {
		t.Fatalf("TTL-capped throttle did not release next payload = %+v, ok=%v err=%v", next, ok, err)
	}
}

func TestAmbiguousCommitFailureHasNoDirectSenderFallback(t *testing.T) {
	f := openAgentSwitchFailureFixture(t)
	// The SQLite store has no observer dependency. A database-wide failure can
	// only return to the saga's retained-settlement path; it cannot invoke a
	// direct semantic sender and risk duplicating a commit-applied outbox row.
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store before injected commit failure: %v", err)
	}
	if _, err := f.store.ApplyAgentSwitchMutation(context.Background(), failedMutation(f)); err == nil {
		t.Fatal("closed database unexpectedly accepted a failure mutation")
	}
	if got := countFailureRows(t, f.db, "agent_switch_failure_outbox"); got != 0 {
		t.Fatalf("outbox rows after ambiguous database failure = %d, want 0", got)
	}
}
