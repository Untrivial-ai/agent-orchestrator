package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/sentryobs"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"

	_ "modernc.org/sqlite"
)

type panicAgentSwitchEventEncoder struct{}

func (panicAgentSwitchEventEncoder) EncodeAgentSwitchFailureEvent(domain.AgentSwitchEventBuildInput) (ports.AgentSwitchFailureEncodedEvent, error) {
	panic("injected canonical builder panic")
}

func openAgentSwitchFailureInternalStore(t *testing.T) (*Store, *sql.DB, domain.AgentSwitch, domain.AgentSwitchFault) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE agent_switches (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, target_native_session_ref TEXT,
 target_start_mode TEXT NOT NULL, state TEXT NOT NULL, source_generation_id TEXT NOT NULL,
 target_generation_id TEXT NOT NULL, target_runtime_handle_id TEXT NOT NULL,
 error_code TEXT NOT NULL, failure_point TEXT NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE TABLE agent_switch_failure_policy (
 singleton INTEGER PRIMARY KEY, enabled INTEGER NOT NULL, consent_generation TEXT NOT NULL,
 destination_fingerprint TEXT NOT NULL, updated_at TIMESTAMP NOT NULL
);
INSERT INTO agent_switch_failure_policy VALUES (1,0,'','',CURRENT_TIMESTAMP);
CREATE TABLE agent_switch_failure_receipts (
 dedupe_key TEXT PRIMARY KEY, switch_id TEXT, report_kind TEXT NOT NULL,
 durable_state_fingerprint TEXT NOT NULL, recorded_at TIMESTAMP NOT NULL, retain_until TIMESTAMP
);
CREATE TABLE agent_switch_failure_outbox (
 id TEXT PRIMARY KEY, schema_version INTEGER NOT NULL, envelope_encoding_version INTEGER NOT NULL,
 dedupe_key TEXT NOT NULL UNIQUE, destination_fingerprint TEXT NOT NULL, switch_id TEXT,
 report_kind TEXT NOT NULL, scope TEXT NOT NULL, failure_point TEXT NOT NULL,
 classifier_callsite TEXT NOT NULL, phase TEXT NOT NULL, error_code TEXT NOT NULL,
 fault_code TEXT NOT NULL, execution TEXT NOT NULL, execution_attempt_id TEXT NOT NULL,
 mode TEXT NOT NULL, from_harness TEXT NOT NULL, target_harness TEXT NOT NULL,
 target_start_mode TEXT NOT NULL, runtime_backend TEXT NOT NULL, call_outcome TEXT NOT NULL,
 ownership TEXT NOT NULL, compensation TEXT NOT NULL, user_impact TEXT NOT NULL,
 source_stop_confirmed TEXT NOT NULL, target_owner_committed TEXT NOT NULL,
 gate_retained TEXT NOT NULL, requested_at TIMESTAMP, occurred_at TIMESTAMP NOT NULL,
 sanitized_stack BLOB NOT NULL, stack_fingerprint TEXT NOT NULL, canonical_event_json BLOB NOT NULL,
 expires_at TIMESTAMP NOT NULL, available_at TIMESTAMP NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
 last_attempt_at TIMESTAMP, lease_token TEXT, lease_consent_generation TEXT,
 lease_delivery_epoch INTEGER, lease_expires_at TIMESTAMP, delivered_at TIMESTAMP,
 discarded_at TIMESTAMP, last_delivery_error_class TEXT NOT NULL DEFAULT ''
);
CREATE TABLE agent_switch_failure_delivery_state (
 destination_fingerprint TEXT PRIMARY KEY, error_not_before TIMESTAMP, all_not_before TIMESTAMP
);`); err != nil {
		t.Fatalf("create focused failure schema: %v", err)
	}

	now := time.Date(2026, time.August, 28, 11, 0, 0, 0, time.UTC)
	sw := domain.AgentSwitch{
		ID: "internal-switch", SessionID: "internal-session", IdempotencyKey: "internal-key",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint("internal-session", domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceTranscriptStatus: domain.AgentSwitchSourceTranscriptNotAttempted,
		SourceGenerationID:     "source-internal", RequestedAt: now, UpdatedAt: now,
	}
	if _, err := db.Exec(`INSERT INTO agent_switches (
 id,session_id,target_native_session_ref,target_start_mode,state,source_generation_id,
 target_generation_id,target_runtime_handle_id,error_code,failure_point,updated_at
) VALUES (?,?,NULL,'','preparing_handoff',?,'','','','',?)`, sw.ID, sw.SessionID, sw.SourceGenerationID, now); err != nil {
		t.Fatalf("seed internal switch: %v", err)
	}
	fault := domain.AgentSwitchFault{
		ReportKind:         domain.AgentSwitchReportTerminalFailure,
		FailurePoint:       domain.AgentSwitchFailureWorkerStartRefused,
		ClassifierCallsite: domain.AgentSwitchClassifierAdmission,
		Phase:              domain.AgentSwitchPreparingHandoff, ErrorCode: domain.AgentSwitchErrorFailedPreStop,
		FaultCode: domain.AgentSwitchFaultNotApplicable, Execution: domain.AgentSwitchExecutionLive,
		Mode: domain.SessionModeTUI, FromHarness: sw.FromHarness, TargetHarness: sw.TargetHarness,
		TargetStartMode: domain.AgentSwitchTargetStartReportedPending,
		RuntimeBackend:  domain.AgentSwitchRuntimeNotApplicable,
		CallOutcome:     domain.AgentSwitchCallNoEffectFailure, Ownership: domain.AgentSwitchOwnershipSource,
		Compensation:        domain.AgentSwitchCompensationNotNeeded,
		UserImpact:          domain.AgentSwitchUserImpactSourceAvailable,
		SourceStopConfirmed: domain.AgentSwitchTriFalse, TargetOwnerCommitted: domain.AgentSwitchTriFalse,
		GateRetained: domain.AgentSwitchTriFalse, OccurredAt: now.Add(time.Second),
	}
	st := NewStore(db, db)
	if err := st.ConfigureAgentSwitchFailureEventEncoder(context.Background(), sentryobs.AgentSwitchEventEncoder{}); err != nil {
		t.Fatalf("configure event encoder: %v", err)
	}
	metadata := domain.AgentSwitchEventMetadata{
		Release: "1.2.3", Environment: domain.AgentSwitchEnvironmentDevelopment,
		Channel: domain.AgentSwitchChannelPreview, Platform: domain.AgentSwitchPlatformDaemon,
		OS: domain.AgentSwitchOSLinux, ElapsedTimeBucket: domain.AgentSwitchElapsedNotApplicable,
	}
	if err := st.ConfigureAgentSwitchFailureEventMetadata(context.Background(), metadata); err != nil {
		t.Fatalf("configure metadata: %v", err)
	}
	authorization := domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation-internal", DestinationFingerprint: "destination-internal"}
	if err := st.ApplyAgentSwitchFailurePolicy(context.Background(), ports.AgentSwitchFailurePolicy{Authorization: authorization, UpdatedAt: now}); err != nil {
		t.Fatalf("enable policy: %v", err)
	}
	return st, db, sw, fault
}

func internalFailedMutation(sw domain.AgentSwitch, fault domain.AgentSwitchFault) ports.AgentSwitchMutation {
	rec := sw
	rec.State = domain.AgentSwitchFailed
	rec.ErrorCode = fault.ErrorCode
	rec.FailurePoint = fault.FailurePoint
	rec.UpdatedAt = fault.OccurredAt
	return ports.AgentSwitchMutation{
		Record: rec, ExpectedState: domain.AgentSwitchPreparingHandoff,
		ExpectedSourceGenerationID: rec.SourceGenerationID, Fault: &fault,
		Authorization: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation-internal", DestinationFingerprint: "destination-internal"},
	}
}

func TestAgentSwitchFailureBuilderPanicRollsBackTelemetryOnly(t *testing.T) {
	st, db, sw, fault := openAgentSwitchFailureInternalStore(t)
	st.agentSwitchFailureEventEncoder = panicAgentSwitchEventEncoder{}
	result, err := st.ApplyAgentSwitchMutation(context.Background(), internalFailedMutation(sw, fault))
	if err != nil || !result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentLocalInvariantFailed {
		t.Fatalf("builder panic result = %+v, err=%v", result, err)
	}
	var state, point string
	if err := db.QueryRow(`SELECT state,failure_point FROM agent_switches WHERE id=?`, sw.ID).Scan(&state, &point); err != nil || state != "failed" || point != string(fault.FailurePoint) {
		t.Fatalf("core facts after builder panic: state=%q point=%q err=%v", state, point, err)
	}
	for _, table := range []string{"agent_switch_failure_receipts", "agent_switch_failure_outbox"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows after builder panic = %d, err=%v", table, count, err)
		}
	}
}

func TestAgentSwitchFailureCommitResponseLossReadbackRetryIsIdempotent(t *testing.T) {
	st, db, sw, fault := openAgentSwitchFailureInternalStore(t)
	st.agentSwitchFailureCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected commit response loss")
	}
	mutation := internalFailedMutation(sw, fault)
	if _, err := st.ApplyAgentSwitchMutation(context.Background(), mutation); err == nil {
		t.Fatal("commit response loss did not surface an ambiguous error")
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM agent_switches WHERE id=?`, sw.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("readback after ambiguous commit: state=%q err=%v", state, err)
	}
	st.agentSwitchFailureCommit = func(tx *sql.Tx) error { return tx.Commit() }
	result, err := st.ApplyAgentSwitchMutation(context.Background(), mutation)
	if err != nil || result.CoreChanged || result.Enrollment != domain.AgentSwitchEnrollmentDeduped {
		t.Fatalf("idempotent retry = %+v, err=%v", result, err)
	}
	for _, table := range []string{"agent_switch_failure_receipts", "agent_switch_failure_outbox"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s rows after ambiguous retry = %d, err=%v", table, count, err)
		}
	}
}
