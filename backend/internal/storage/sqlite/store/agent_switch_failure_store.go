package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

const (
	agentSwitchFailureSchemaVersion = 1
	agentSwitchFailureTTL           = 7 * 24 * time.Hour
)

var (
	_ ports.AgentSwitchFaultStore                = (*Store)(nil)
	_ ports.AgentSwitchFailureOutboxStore        = (*Store)(nil)
	_ ports.AgentSwitchFailureEventMetadataStore = (*Store)(nil)
)

// ConfigureAgentSwitchFailureEventMetadata sets the process-local metadata used for new failure events.
func (s *Store) ConfigureAgentSwitchFailureEventMetadata(ctx context.Context, metadata domain.AgentSwitchEventMetadata) error {
	if err := domain.ValidateAgentSwitchEventMetadata(metadata); err != nil {
		return fmt.Errorf("configure agent switch failure event metadata: %w", err)
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	metadataCopy := metadata
	s.agentSwitchFailureEventMetadata = &metadataCopy
	return nil
}

// ConfigureAgentSwitchFailureEventEncoder injects the provider adapter used to
// freeze immutable outbox bytes.
func (s *Store) ConfigureAgentSwitchFailureEventEncoder(ctx context.Context, encoder ports.AgentSwitchFailureEventEncoder) error {
	if encoder == nil {
		return errors.New("configure agent switch failure event encoder: encoder is required")
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	s.agentSwitchFailureEventEncoder = encoder
	return nil
}

// ApplyAgentSwitchMutation atomically persists a switch mutation and its eligible failure event.
func (s *Store) ApplyAgentSwitchMutation(ctx context.Context, mutation ports.AgentSwitchMutation) (ports.AgentSwitchMutationResult, error) {
	return s.applyAgentSwitchMutation(ctx, mutation, false)
}

// FailAgentSwitchIfUnacknowledgedWithFault fails an unacknowledged switch and enrolls its eligible fault atomically.
func (s *Store) FailAgentSwitchIfUnacknowledgedWithFault(ctx context.Context, mutation ports.AgentSwitchMutation) (ports.AgentSwitchMutationResult, error) {
	return s.applyAgentSwitchMutation(ctx, mutation, true)
}

func (s *Store) applyAgentSwitchMutation(ctx context.Context, mutation ports.AgentSwitchMutation, unacknowledged bool) (ports.AgentSwitchMutationResult, error) {
	rec := mutation.Record
	if rec.ErrorCode == "" {
		rec.FailurePoint = ""
	}
	normalizeAgentSwitchMutationPoint(&rec)
	mutation.Record = rec
	if err := validateAgentSwitchCoreMutation(mutation, unacknowledged); err != nil {
		return ports.AgentSwitchMutationResult{}, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("begin agent switch fault mutation %s: %w", rec.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	if err := ensureNativeSessionRefBelongsTo(ctx, q, rec.SessionID, rec.TargetHarness, rec.TargetNativeSessionRef, "target"); err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("apply agent switch mutation %s: %w", rec.ID, err)
	}

	var changed int64
	if unacknowledged {
		changed, err = q.FailAgentSwitchIfUnacknowledged(ctx, gen.FailAgentSwitchIfUnacknowledgedParams{
			ErrorCode: string(rec.ErrorCode), FailurePoint: string(rec.FailurePoint), FailedAt: rec.UpdatedAt,
			ID: rec.ID, SessionID: rec.SessionID,
			ExpectedSourceGenerationID: mutation.ExpectedSourceGenerationID,
			ExpectedTargetGenerationID: mutation.ExpectedTargetGenerationID,
		})
	} else {
		changed, err = q.UpdateAgentSwitch(ctx, gen.UpdateAgentSwitchParams{
			TargetNativeSessionRef: rec.TargetNativeSessionRef, TargetStartMode: rec.TargetStartMode,
			NextState: rec.State, NextTargetGenerationID: rec.TargetGenerationID,
			NextTargetRuntimeHandleID: rec.TargetRuntimeHandleID, ErrorCode: string(rec.ErrorCode),
			FailurePoint: string(rec.FailurePoint), UpdatedAt: rec.UpdatedAt,
			ID: rec.ID, SessionID: rec.SessionID, ExpectedState: mutation.ExpectedState,
			ExpectedSourceGenerationID: mutation.ExpectedSourceGenerationID,
			ExpectedTargetGenerationID: mutation.ExpectedTargetGenerationID,
		})
	}
	if err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("apply agent switch mutation %s: %w", rec.ID, err)
	}
	if changed == 0 {
		if err := tx.Commit(); err != nil {
			return ports.AgentSwitchMutationResult{}, fmt.Errorf("commit unchanged agent switch mutation %s: %w", rec.ID, err)
		}
		return ports.AgentSwitchMutationResult{Enrollment: domain.AgentSwitchEnrollmentDeduped}, nil
	}

	result := ports.AgentSwitchMutationResult{CoreChanged: true, Enrollment: domain.AgentSwitchEnrollmentDeduped}
	result.Enrollment = s.enrollFaultSavepoint(ctx, tx, failureEnrollmentInput{
		Switch: &rec, Fault: mutation.Fault, Authorization: mutation.Authorization,
		FaultPhase: mutation.ExpectedState, ResolveReceipts: true,
	})
	if err := s.agentSwitchFailureCommit(tx); err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("commit agent switch fault mutation %s: %w", rec.ID, err)
	}
	return result, nil
}

func normalizeAgentSwitchMutationPoint(rec *domain.AgentSwitch) {
	point := rec.FailurePoint
	if point == "" {
		return
	}
	if _, ok := domain.AgentSwitchFailureTaxonomy(point); !ok {
		point = domain.AgentSwitchFailureClassificationUnknown
	}
	rec.FailurePoint = point
}

func validateAgentSwitchCoreMutation(m ports.AgentSwitchMutation, unacknowledged bool) error {
	rec := m.Record
	if err := validateAgentSwitch(rec, false); err != nil {
		return err
	}
	if !m.ExpectedState.Valid() || !domain.ValidAgentSwitchTransition(m.ExpectedState, rec.State) {
		return fmt.Errorf("apply agent switch mutation %s: invalid transition %q -> %q", rec.ID, m.ExpectedState, rec.State)
	}
	if m.ExpectedSourceGenerationID == "" || rec.SourceGenerationID != m.ExpectedSourceGenerationID {
		return fmt.Errorf("apply agent switch mutation %s: source generation does not match immutable provenance", rec.ID)
	}
	if unacknowledged {
		if rec.State != domain.AgentSwitchFailed || m.ExpectedState != domain.AgentSwitchDelivering || rec.TargetGenerationID == "" || rec.TargetAcknowledgedAt != nil {
			return fmt.Errorf("fail unacknowledged agent switch %s: exact unacknowledged delivery facts are required", rec.ID)
		}
	} else if m.ExpectedState == domain.AgentSwitchDelivering && rec.State == domain.AgentSwitchFailed {
		return fmt.Errorf("apply agent switch mutation %s: delivery failure requires the acknowledgement-fenced operation", rec.ID)
	}
	return nil
}

type failureEnrollmentInput struct {
	Switch             *domain.AgentSwitch
	DaemonRunID        string
	Fault              *domain.AgentSwitchFault
	Authorization      domain.AgentSwitchReportingAuthorization
	GuardCurrentSwitch bool
	ResolveReceipts    bool
	FaultPhase         domain.AgentSwitchState
}

func (s *Store) enrollFaultSavepoint(ctx context.Context, tx *sql.Tx, input failureEnrollmentInput) (status domain.AgentSwitchEnrollmentStatus) {
	status = domain.AgentSwitchEnrollmentLocalInvariantFailed
	if _, err := tx.ExecContext(ctx, `SAVEPOINT agent_switch_telemetry`); err != nil {
		logAgentSwitchEnrollmentInvariant(input.Fault, "savepoint_begin")
		return status
	}
	released := false
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = tx.ExecContext(ctx, `ROLLBACK TO agent_switch_telemetry`)
			_, _ = tx.ExecContext(ctx, `RELEASE agent_switch_telemetry`)
			logAgentSwitchEnrollmentInvariant(input.Fault, "builder_panic")
			status = domain.AgentSwitchEnrollmentLocalInvariantFailed
			return
		}
		if !released {
			_, _ = tx.ExecContext(ctx, `ROLLBACK TO agent_switch_telemetry`)
			_, _ = tx.ExecContext(ctx, `RELEASE agent_switch_telemetry`)
		}
	}()

	status, err := s.enrollFaultTx(ctx, tx, input)
	if err != nil {
		logAgentSwitchEnrollmentInvariant(input.Fault, "validate_serialize_or_insert")
		return domain.AgentSwitchEnrollmentLocalInvariantFailed
	}
	if _, err := tx.ExecContext(ctx, `RELEASE agent_switch_telemetry`); err != nil {
		logAgentSwitchEnrollmentInvariant(input.Fault, "savepoint_release")
		return domain.AgentSwitchEnrollmentLocalInvariantFailed
	}
	released = true
	return status
}

func logAgentSwitchEnrollmentInvariant(fault *domain.AgentSwitchFault, stage string) {
	point, kind := domain.AgentSwitchFailurePoint(""), domain.AgentSwitchReportKind("")
	if fault != nil {
		point, kind = fault.FailurePoint, fault.ReportKind
	}
	slog.Default().Error("agent switch telemetry local invariant", "stage", stage,
		"failure_point", point, "report_kind", kind)
}

func (s *Store) enrollFaultTx(ctx context.Context, tx *sql.Tx, input failureEnrollmentInput) (domain.AgentSwitchEnrollmentStatus, error) {
	if err := validateFailureEnrollment(input); err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	q := gen.New(tx)
	if input.ResolveReceipts && input.Switch != nil {
		if _, err := q.ResolveAgentSwitchFailureReceipts(ctx, gen.ResolveAgentSwitchFailureReceiptsParams{
			RetainUntil:             sql.NullTime{Time: input.Switch.UpdatedAt.Add(agentSwitchFailureTTL), Valid: true},
			SwitchID:                sql.NullString{String: string(input.Switch.ID), Valid: true},
			DurableStateFingerprint: agentSwitchDurableFingerprint(*input.Switch),
		}); err != nil {
			return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
		}
	}
	if input.Fault == nil {
		return domain.AgentSwitchEnrollmentDeduped, nil
	}
	fault := *input.Fault
	if !input.Authorization.Enabled {
		return domain.AgentSwitchEnrollmentDisabled, nil
	}
	policy, err := q.GetAgentSwitchFailurePolicy(ctx)
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	if !policy.Enabled {
		return domain.AgentSwitchEnrollmentDisabled, nil
	}
	generation, destination := policy.ConsentGeneration, policy.DestinationFingerprint
	if generation != input.Authorization.ConsentGeneration || destination != input.Authorization.DestinationFingerprint {
		return domain.AgentSwitchEnrollmentStaleGeneration, nil
	}

	scope := domain.AgentSwitchDedupeScope{DaemonRunID: input.DaemonRunID}
	scopeName := "daemon"
	var switchID sql.NullString
	var requestedAt sql.NullTime
	durableFingerprint := "daemon|" + input.DaemonRunID
	if input.Switch != nil {
		scope.SwitchID = input.Switch.ID
		scopeName = "switch"
		switchID = sql.NullString{String: string(input.Switch.ID), Valid: true}
		requestedAt = sql.NullTime{Time: input.Switch.RequestedAt, Valid: true}
		durableFingerprint = agentSwitchDurableFingerprint(*input.Switch)
	}
	dedupeKey, err := domain.AgentSwitchDedupeKey(scope, fault)
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	eventID := domain.StableAgentSwitchEventID(dedupeKey)
	if s.agentSwitchFailureEventMetadata == nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, errors.New("agent switch failure event metadata is not configured")
	}
	metadata := *s.agentSwitchFailureEventMetadata
	if err := domain.ValidateAgentSwitchEventMetadata(metadata); err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	if s.agentSwitchFailureEventEncoder == nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, errors.New("agent switch failure event encoder is not configured")
	}
	encoded, err := s.agentSwitchFailureEventEncoder.EncodeAgentSwitchFailureEvent(domain.AgentSwitchEventBuildInput{
		EventID: eventID, Fault: fault, Release: metadata.Release,
		Environment: metadata.Environment, Channel: metadata.Channel,
		Platform: metadata.Platform, OS: metadata.OS,
		ElapsedTimeBucket: metadata.ElapsedTimeBucket,
	})
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	frames, err := json.Marshal(fault.Frames)
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	retainUntil := sql.NullTime{}
	if fault.ReportKind != domain.AgentSwitchReportRecoveryRequired && fault.ReportKind != domain.AgentSwitchReportRecoveryAttemptFailed {
		retainUntil = sql.NullTime{Time: fault.OccurredAt.Add(agentSwitchFailureTTL), Valid: true}
	}

	var receiptRows int64
	if input.GuardCurrentSwitch && input.Switch != nil {
		receiptRows, err = q.InsertAgentSwitchFailureReceiptForCurrentSwitch(ctx, gen.InsertAgentSwitchFailureReceiptForCurrentSwitchParams{
			DedupeKey: dedupeKey, ReportKind: string(fault.ReportKind),
			DurableStateFingerprint: durableFingerprint, RecordedAt: fault.OccurredAt,
			RetainUntil: retainUntil, SwitchID: input.Switch.ID, ExpectedState: input.Switch.State,
			ExpectedErrorCode: string(input.Switch.ErrorCode), ExpectedFailurePoint: string(input.Switch.FailurePoint),
			ExpectedUpdatedAt: input.Switch.UpdatedAt, ConsentGeneration: generation,
			DestinationFingerprint: destination,
		})
	} else {
		receiptRows, err = q.InsertAgentSwitchFailureReceipt(ctx, gen.InsertAgentSwitchFailureReceiptParams{
			DedupeKey: dedupeKey, SwitchID: switchID, ReportKind: string(fault.ReportKind),
			DurableStateFingerprint: durableFingerprint, RecordedAt: fault.OccurredAt,
			RetainUntil: retainUntil, ConsentGeneration: generation, DestinationFingerprint: destination,
		})
	}
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	if receiptRows == 0 {
		return domain.AgentSwitchEnrollmentDeduped, nil
	}
	expiresAt := fault.OccurredAt.Add(agentSwitchFailureTTL)
	payloadRows, err := q.InsertAgentSwitchFailurePayload(ctx, gen.InsertAgentSwitchFailurePayloadParams{
		ID: eventID, SchemaVersion: agentSwitchFailureSchemaVersion,
		EnvelopeEncodingVersion: int64(encoded.EnvelopeEncodingVersion),
		DedupeKey:               dedupeKey, DestinationFingerprint: destination, SwitchID: switchID,
		ReportKind: string(fault.ReportKind), Scope: scopeName,
		FailurePoint: string(fault.FailurePoint), ClassifierCallsite: string(fault.ClassifierCallsite),
		Phase: string(fault.Phase), ErrorCode: string(fault.ErrorCode), FaultCode: string(fault.FaultCode),
		Execution: string(fault.Execution), ExecutionAttemptID: fault.ExecutionAttemptID,
		Mode: string(fault.Mode), FromHarness: string(fault.FromHarness), TargetHarness: string(fault.TargetHarness),
		TargetStartMode: string(fault.TargetStartMode), RuntimeBackend: string(fault.RuntimeBackend),
		CallOutcome: string(fault.CallOutcome), Ownership: string(fault.Ownership),
		Compensation: string(fault.Compensation), UserImpact: string(fault.UserImpact),
		SourceStopConfirmed: string(fault.SourceStopConfirmed), TargetOwnerCommitted: string(fault.TargetOwnerCommitted),
		GateRetained: string(fault.GateRetained), RequestedAt: requestedAt, OccurredAt: fault.OccurredAt,
		SanitizedStack: frames, StackFingerprint: domain.AgentSwitchStackFingerprint(fault.Frames),
		CanonicalEventJson: encoded.Payload, ExpiresAt: expiresAt, AvailableAt: fault.OccurredAt,
		ConsentGeneration: generation,
	})
	if err != nil {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, err
	}
	if payloadRows != 1 {
		return domain.AgentSwitchEnrollmentLocalInvariantFailed, errors.New("outbox insert did not match the enrolled receipt")
	}
	return domain.AgentSwitchEnrollmentEnrolled, nil
}

func validateFailureEnrollment(input failureEnrollmentInput) error {
	if input.Switch != nil {
		reportable := input.Switch.State == domain.AgentSwitchFailed || input.Switch.ErrorCode.RetainedRecoveryMarker()
		if reportable && input.Fault == nil {
			return errors.New("reportable agent switch mutation requires a typed fault")
		}
		if !reportable && input.Fault != nil {
			return errors.New("ordinary agent switch progress cannot carry a fault")
		}
	}
	if input.Fault == nil {
		return nil
	}
	if err := domain.ValidateAgentSwitchFault(*input.Fault); err != nil {
		return err
	}
	if input.Switch == nil {
		return nil
	}
	binding := *input.Switch
	if input.FaultPhase != "" {
		binding.State = input.FaultPhase
	}
	return validateAgentSwitchFaultBinding(binding, *input.Fault, false)
}

func agentSwitchDurableFingerprint(sw domain.AgentSwitch) string {
	value := strings.Join([]string{string(sw.State), string(sw.ErrorCode), string(sw.FailurePoint)}, "|")
	digest := sha256.Sum256([]byte(value))
	return "v1:" + hex.EncodeToString(digest[:])
}

func validateAgentSwitchFaultBinding(sw domain.AgentSwitch, fault domain.AgentSwitchFault, strictSemantic bool) error {
	if fault.Phase != sw.State {
		return fmt.Errorf("fault phase %q does not match durable state %q", fault.Phase, sw.State)
	}
	if fault.FromHarness != sw.FromHarness || fault.TargetHarness != sw.TargetHarness {
		return errors.New("fault harness direction does not match durable switch")
	}
	expectedStart := sw.TargetStartMode
	if expectedStart == "" {
		expectedStart = domain.AgentSwitchTargetStartReportedPending
	}
	if fault.TargetStartMode != expectedStart {
		return fmt.Errorf("fault target start mode %q does not match durable mode %q", fault.TargetStartMode, expectedStart)
	}
	semantic := strictSemantic || fault.ReportKind == domain.AgentSwitchReportTerminalFailure || fault.ReportKind == domain.AgentSwitchReportRecoveryRequired
	if semantic {
		pointMatches := fault.FailurePoint == sw.FailurePoint
		if sw.FailurePoint == "" && fault.FailurePoint == domain.AgentSwitchFailureRecoveryExistingMarker {
			pointMatches = true
		}
		if !pointMatches {
			return fmt.Errorf("fault failure point %q does not match durable point %q", fault.FailurePoint, sw.FailurePoint)
		}
		if fault.ErrorCode != sw.ErrorCode {
			return fmt.Errorf("fault error code %q does not match durable code %q", fault.ErrorCode, sw.ErrorCode)
		}
	} else if fault.ReportKind == domain.AgentSwitchReportRecoveryAttemptFailed || fault.ReportKind == domain.AgentSwitchReportMaintenanceFailure {
		if fault.ErrorCode != sw.ErrorCode {
			return fmt.Errorf("fault error code %q does not match durable code %q", fault.ErrorCode, sw.ErrorCode)
		}
	}
	return nil
}

// EnqueueAgentSwitchOperationalFault enrolls an operational fault against an unchanged durable switch fingerprint.
func (s *Store) EnqueueAgentSwitchOperationalFault(ctx context.Context, input ports.AgentSwitchOperationalFault) (ports.AgentSwitchMutationResult, error) {
	if input.SwitchID == "" || input.ExpectedUpdatedAt.IsZero() {
		return ports.AgentSwitchMutationResult{}, errors.New("enqueue agent switch operational fault: durable switch fingerprint is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("begin standalone agent switch fault: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `
SELECT id,session_id,idempotency_key,request_fingerprint,from_harness,target_harness,
 target_native_session_ref,target_start_mode,state,agent_handoff_status,source_transcript_status,
 semantic_handoff_included,agent_handoff_path,agent_handoff_hash,source_generation_id,
 target_generation_id,target_runtime_handle_id,target_acknowledged_at,error_code,failure_point,
 requested_at,updated_at,final_handoff_path,final_handoff_hash
FROM agent_switches WHERE id=? AND state=? AND error_code=? AND failure_point=? AND updated_at=?`,
		input.SwitchID, input.ExpectedState, input.ExpectedErrorCode, input.ExpectedFailurePoint, input.ExpectedUpdatedAt)
	sw, found, err := scanAgentSwitchFailureFingerprint(row)
	if err != nil {
		return ports.AgentSwitchMutationResult{}, err
	}
	if !found {
		if err := tx.Commit(); err != nil {
			return ports.AgentSwitchMutationResult{}, err
		}
		return ports.AgentSwitchMutationResult{Enrollment: domain.AgentSwitchEnrollmentDeduped}, nil
	}
	if err := domain.ValidateAgentSwitchFault(input.Fault); err == nil {
		if err := validateAgentSwitchFaultBinding(sw, input.Fault, false); err != nil {
			return ports.AgentSwitchMutationResult{}, fmt.Errorf("enqueue agent switch operational fault: %w", err)
		}
	}
	status := s.enrollFaultSavepoint(ctx, tx, failureEnrollmentInput{
		Switch: &sw, DaemonRunID: input.DaemonRunID, Fault: &input.Fault, Authorization: input.Authorization,
		GuardCurrentSwitch: true,
	})
	if err := tx.Commit(); err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("commit standalone agent switch fault: %w", err)
	}
	return ports.AgentSwitchMutationResult{CoreChanged: status == domain.AgentSwitchEnrollmentEnrolled, Enrollment: status}, nil
}

func scanAgentSwitchFailureFingerprint(row *sql.Row) (domain.AgentSwitch, bool, error) {
	var sw domain.AgentSwitch
	var targetRef sql.NullString
	var acknowledged sql.NullTime
	err := row.Scan(&sw.ID, &sw.SessionID, &sw.IdempotencyKey, &sw.RequestFingerprint,
		&sw.FromHarness, &sw.TargetHarness, &targetRef, &sw.TargetStartMode, &sw.State,
		&sw.AgentHandoffStatus, &sw.SourceTranscriptStatus, &sw.SemanticHandoffIncluded,
		&sw.AgentHandoffPath, &sw.AgentHandoffHash, &sw.SourceGenerationID, &sw.TargetGenerationID,
		&sw.TargetRuntimeHandleID, &acknowledged, &sw.ErrorCode, &sw.FailurePoint,
		&sw.RequestedAt, &sw.UpdatedAt, &sw.FinalHandoffPath, &sw.FinalHandoffHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgentSwitch{}, false, fmt.Errorf("read current switch fingerprint: %w", err)
	}
	if targetRef.Valid {
		ref := domain.AgentNativeSessionID(targetRef.String)
		sw.TargetNativeSessionRef = &ref
	}
	if acknowledged.Valid {
		sw.TargetAcknowledgedAt = &acknowledged.Time
	}
	return sw, true, nil
}

// EnqueueAgentSwitchDaemonFault enrolls a daemon-owned failure that is not bound to a switch row.
func (s *Store) EnqueueAgentSwitchDaemonFault(ctx context.Context, input ports.AgentSwitchDaemonFault) (ports.AgentSwitchMutationResult, error) {
	if strings.TrimSpace(input.DaemonRunID) == "" {
		return ports.AgentSwitchMutationResult{}, errors.New("enqueue daemon agent switch fault: daemon run ID is required")
	}
	if err := domain.ValidateAgentSwitchFault(input.Fault); err == nil {
		if input.Fault.Phase != domain.AgentSwitchStateNotApplicable || input.Fault.ErrorCode != domain.AgentSwitchErrorNotApplicable ||
			input.Fault.Mode != domain.SessionModeNotApplicable || input.Fault.FromHarness != domain.HarnessNotApplicable ||
			input.Fault.TargetHarness != domain.HarnessNotApplicable || input.Fault.TargetStartMode != domain.AgentSwitchTargetStartNotApplicable ||
			input.Fault.RuntimeBackend != domain.AgentSwitchRuntimeNotApplicable || input.Fault.Ownership != domain.AgentSwitchOwnershipNotApplicable ||
			input.Fault.Compensation != domain.AgentSwitchCompensationNotApplicable || input.Fault.UserImpact != domain.AgentSwitchUserImpactNotApplicable ||
			input.Fault.SourceStopConfirmed != domain.AgentSwitchTriNotApplicable || input.Fault.TargetOwnerCommitted != domain.AgentSwitchTriNotApplicable ||
			input.Fault.GateRetained != domain.AgentSwitchTriNotApplicable {
			return ports.AgentSwitchMutationResult{}, errors.New("enqueue daemon agent switch fault: switch-only fields must be explicit not_applicable")
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return ports.AgentSwitchMutationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	status := s.enrollFaultSavepoint(ctx, tx, failureEnrollmentInput{
		DaemonRunID: input.DaemonRunID, Fault: &input.Fault, Authorization: input.Authorization,
	})
	if err := tx.Commit(); err != nil {
		return ports.AgentSwitchMutationResult{}, fmt.Errorf("commit daemon agent switch fault: %w", err)
	}
	return ports.AgentSwitchMutationResult{CoreChanged: status == domain.AgentSwitchEnrollmentEnrolled, Enrollment: status}, nil
}

// ForceDisableAgentSwitchFailurePolicy disables failure delivery without requiring the current consent generation.
func (s *Store) ForceDisableAgentSwitchFailurePolicy(ctx context.Context, updatedAt time.Time) error {
	if updatedAt.IsZero() {
		return errors.New("force-disable agent switch failure policy: timestamp is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.ForceDisableAgentSwitchFailurePolicy(ctx, updatedAt)
	return err
}

// ApplyAgentSwitchFailurePolicy persists delivery authorization and purges payloads when disabling it.
func (s *Store) ApplyAgentSwitchFailurePolicy(ctx context.Context, policy ports.AgentSwitchFailurePolicy) error {
	if policy.UpdatedAt.IsZero() {
		return errors.New("apply agent switch failure policy: timestamp is required")
	}
	if policy.Authorization.Enabled && (policy.Authorization.ConsentGeneration == "" || policy.Authorization.DestinationFingerprint == "") {
		return errors.New("apply agent switch failure policy: enabled policy requires generation and destination")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	if _, err := q.ApplyAgentSwitchFailurePolicy(ctx, gen.ApplyAgentSwitchFailurePolicyParams{
		Enabled: policy.Authorization.Enabled, ConsentGeneration: policy.Authorization.ConsentGeneration,
		DestinationFingerprint: policy.Authorization.DestinationFingerprint, UpdatedAt: policy.UpdatedAt,
	}); err != nil {
		return err
	}
	if !policy.Authorization.Enabled {
		if _, err := q.PurgeAgentSwitchFailurePayloads(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeAgentSwitchFailurePayloads deletes every queued failure payload.
func (s *Store) PurgeAgentSwitchFailurePayloads(ctx context.Context) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.PurgeAgentSwitchFailurePayloads(ctx)
}

// EnrollCurrentAgentSwitchRecoveryMarkers enrolls eligible recovery markers that remain current.
func (s *Store) EnrollCurrentAgentSwitchRecoveryMarkers(ctx context.Context, input ports.AgentSwitchFailureRecoveryEnrollment) (int64, error) {
	if input.EnrolledAt.IsZero() {
		return 0, errors.New("enroll current agent switch recovery markers: timestamp is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	rows, err := q.ListCurrentAgentSwitchRecoveryMarkers(ctx)
	if err != nil {
		return 0, err
	}
	var enrolled int64
	for _, m := range rows {
		point := domain.AgentSwitchFailurePoint(m.FailurePoint)
		if point == "" {
			point = domain.AgentSwitchFailureRecoveryExistingMarker
		}
		entry, ok := domain.AgentSwitchFailureTaxonomy(point)
		if !ok || entry.LocalOnly {
			continue
		}
		backend := domain.AgentSwitchRuntimeTMUX
		if m.SessionMode == domain.SessionModeChat {
			backend = domain.AgentSwitchRuntimeChatController
		}
		start := m.TargetStartMode
		if m.State == domain.AgentSwitchStartingTarget && start == domain.AgentSwitchTargetStartPending {
			continue
		}
		if start == "" {
			start = domain.AgentSwitchTargetStartReportedPending
		}
		fault := domain.AgentSwitchFault{
			ReportKind: domain.AgentSwitchReportRecoveryRequired, FailurePoint: point,
			ClassifierCallsite: entry.ClassifierCallsite, Phase: m.State,
			ErrorCode: domain.AgentSwitchErrorCode(m.ErrorCode), FaultCode: domain.AgentSwitchFaultNotApplicable,
			Execution: domain.AgentSwitchExecutionExplicitRecovery, Mode: m.SessionMode,
			FromHarness: m.FromHarness, TargetHarness: m.TargetHarness, TargetStartMode: start,
			RuntimeBackend: backend, CallOutcome: domain.AgentSwitchCallEffectUnknown,
			Ownership: domain.AgentSwitchOwnershipAmbiguous, Compensation: domain.AgentSwitchCompensationUncertain,
			UserImpact: domain.AgentSwitchUserImpactGateRetained, SourceStopConfirmed: domain.AgentSwitchTriFalse,
			TargetOwnerCommitted: domain.AgentSwitchTriFalse, GateRetained: domain.AgentSwitchTriTrue,
			OccurredAt: input.EnrolledAt,
			Frames:     []domain.AgentSwitchStackFrame{{Package: "storage.sqlite.store", Function: "Store.EnrollCurrentAgentSwitchRecoveryMarkers", Filename: "backend/internal/storage/sqlite/store/agent_switch_failure_store.go", Line: 1}},
		}
		sw := domain.AgentSwitch{
			ID: m.ID, SessionID: m.SessionID, State: m.State, ErrorCode: domain.AgentSwitchErrorCode(m.ErrorCode),
			FailurePoint: domain.AgentSwitchFailurePoint(m.FailurePoint), FromHarness: m.FromHarness, TargetHarness: m.TargetHarness,
			TargetStartMode: m.TargetStartMode, RequestedAt: m.RequestedAt, UpdatedAt: m.UpdatedAt,
		}
		status := s.enrollFaultSavepoint(ctx, tx, failureEnrollmentInput{Switch: &sw, Fault: &fault, Authorization: input.Authorization, GuardCurrentSwitch: true})
		if status == domain.AgentSwitchEnrollmentEnrolled {
			enrolled++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return enrolled, nil
}

// ClaimAgentSwitchFailure leases the next authorized failure payload for delivery.
func (s *Store) ClaimAgentSwitchFailure(ctx context.Context, input ports.AgentSwitchFailureClaimRequest) (ports.AgentSwitchFailureClaim, bool, error) {
	if !input.Authorization.Enabled || input.LeaseToken == "" || input.Now.IsZero() || !input.LeaseExpiresAt.After(input.Now) {
		return ports.AgentSwitchFailureClaim{}, false, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	if _, err := q.QuarantineAgentSwitchFailureDestinationMismatch(ctx, gen.QuarantineAgentSwitchFailureDestinationMismatchParams{
		Now: sql.NullTime{Time: input.Now, Valid: true}, DestinationFingerprint: input.Authorization.DestinationFingerprint,
		ConsentGeneration: input.Authorization.ConsentGeneration,
	}); err != nil {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	row, err := q.SelectClaimableAgentSwitchFailure(ctx, gen.SelectClaimableAgentSwitchFailureParams{
		ConsentGeneration:      input.Authorization.ConsentGeneration,
		DestinationFingerprint: input.Authorization.DestinationFingerprint, Now: input.Now,
	})
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ports.AgentSwitchFailureClaim{}, false, err
		}
		return ports.AgentSwitchFailureClaim{}, false, nil
	}
	if err != nil {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	claim := ports.AgentSwitchFailureClaim{
		ID: row.ID, DestinationFingerprint: row.DestinationFingerprint, ExpiresAt: row.ExpiresAt,
		AttemptCount: row.AttemptCount,
		Event:        domain.AgentSwitchFailureEvent{EventID: row.ID, EnvelopeEncodingVersion: int(row.EnvelopeEncodingVersion), CanonicalEventJSON: row.CanonicalEventJson},
	}
	n, err := q.LeaseAgentSwitchFailure(ctx, gen.LeaseAgentSwitchFailureParams{
		LeaseToken:        sql.NullString{String: input.LeaseToken, Valid: true},
		ConsentGeneration: sql.NullString{String: input.Authorization.ConsentGeneration, Valid: true},
		DeliveryEpoch:     sql.NullInt64{Int64: input.DeliveryEpoch, Valid: true},
		LeaseExpiresAt:    sql.NullTime{Time: input.LeaseExpiresAt, Valid: true}, ID: claim.ID,
		DestinationFingerprint: claim.DestinationFingerprint, Now: input.Now,
	})
	if err != nil {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	if n != 1 {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	claim.LeaseToken = input.LeaseToken
	claim.ConsentGeneration = input.Authorization.ConsentGeneration
	claim.DeliveryEpoch = input.DeliveryEpoch
	if err := tx.Commit(); err != nil {
		return ports.AgentSwitchFailureClaim{}, false, err
	}
	return claim, true, nil
}

// BeginAgentSwitchFailureAttempt records the start of a still-authorized leased delivery attempt.
func (s *Store) BeginAgentSwitchFailureAttempt(ctx context.Context, input ports.AgentSwitchFailureAttempt) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.BeginAgentSwitchFailureAttempt(ctx, gen.BeginAgentSwitchFailureAttemptParams{
		Now: sql.NullTime{Time: input.Now, Valid: true}, ID: input.ID,
		LeaseToken:             sql.NullString{String: input.LeaseToken, Valid: true},
		ConsentGeneration:      sql.NullString{String: input.ConsentGeneration, Valid: true},
		DeliveryEpoch:          sql.NullInt64{Int64: input.DeliveryEpoch, Valid: true},
		DestinationFingerprint: input.DestinationFingerprint,
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SettleAgentSwitchFailureDelivery atomically records the outcome of a leased delivery attempt.
func (s *Store) SettleAgentSwitchFailureDelivery(ctx context.Context, input ports.AgentSwitchFailureSettlement) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	leaseToken := sql.NullString{String: input.LeaseToken, Valid: true}
	consentGeneration := sql.NullString{String: input.ConsentGeneration, Valid: true}
	deliveryEpoch := sql.NullInt64{Int64: input.DeliveryEpoch, Valid: true}
	retryNotBefore := input.Result.RetryNotBefore
	var n int64
	switch input.Result.Outcome {
	case ports.DeliveryAccepted:
		n, err = q.MarkAgentSwitchFailureDelivered(ctx, gen.MarkAgentSwitchFailureDeliveredParams{
			SettledAt: sql.NullTime{Time: input.SettledAt, Valid: true}, ID: input.ID,
			LeaseToken: leaseToken, ConsentGeneration: consentGeneration, DeliveryEpoch: deliveryEpoch,
			DestinationFingerprint: input.DestinationFingerprint,
		})
	case ports.DeliveryTransientFailure:
		expiresAt, expiresErr := q.GetLeasedAgentSwitchFailureExpiresAt(ctx, gen.GetLeasedAgentSwitchFailureExpiresAtParams{
			ID: input.ID, LeaseToken: leaseToken, ConsentGeneration: consentGeneration,
			DeliveryEpoch: deliveryEpoch, DestinationFingerprint: input.DestinationFingerprint,
		})
		if errors.Is(expiresErr, sql.ErrNoRows) {
			return false, nil
		} else if expiresErr != nil {
			return false, expiresErr
		}
		next := input.NextAvailableAt
		if retryNotBefore.After(next) {
			next = retryNotBefore
		}
		if next.After(expiresAt) {
			next = expiresAt
		}
		if input.Result.ThrottleScope != ports.DeliveryThrottleNone {
			retryNotBefore = next
		}
		n, err = q.RetryAgentSwitchFailure(ctx, gen.RetryAgentSwitchFailureParams{
			AvailableAt: next, ErrorClass: string(input.Result.Class), ID: input.ID,
			LeaseToken: leaseToken, ConsentGeneration: consentGeneration, DeliveryEpoch: deliveryEpoch,
			DestinationFingerprint: input.DestinationFingerprint,
		})
	case ports.DeliveryPermanentFailure:
		n, err = q.DiscardAgentSwitchFailure(ctx, gen.DiscardAgentSwitchFailureParams{
			SettledAt: sql.NullTime{Time: input.SettledAt, Valid: true}, ErrorClass: string(input.Result.Class), ID: input.ID,
			LeaseToken: leaseToken, ConsentGeneration: consentGeneration, DeliveryEpoch: deliveryEpoch,
			DestinationFingerprint: input.DestinationFingerprint,
		})
	case ports.DeliveryPolicyCancelled, ports.DeliveryShutdownCancelled:
		n, err = q.ReleaseAgentSwitchFailureLease(ctx, gen.ReleaseAgentSwitchFailureLeaseParams{
			ID: input.ID, LeaseToken: leaseToken, ConsentGeneration: consentGeneration,
			DeliveryEpoch: deliveryEpoch, DestinationFingerprint: input.DestinationFingerprint,
		})
	default:
		return false, errors.New("settle agent switch failure delivery: invalid outcome")
	}
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	if retryNotBefore.After(input.SettledAt) && input.Result.ThrottleScope != ports.DeliveryThrottleNone {
		var errorUntil, allUntil sql.NullTime
		if input.Result.ThrottleScope == ports.DeliveryThrottleErrorCategory {
			errorUntil = sql.NullTime{Time: retryNotBefore, Valid: true}
		} else {
			allUntil = sql.NullTime{Time: retryNotBefore, Valid: true}
		}
		if err := q.UpsertAgentSwitchFailureThrottle(ctx, gen.UpsertAgentSwitchFailureThrottleParams{
			DestinationFingerprint: input.DestinationFingerprint, ErrorNotBefore: errorUntil, AllNotBefore: allUntil,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ExpireAgentSwitchFailurePayloads deletes queued payloads whose retention window has elapsed.
func (s *Store) ExpireAgentSwitchFailurePayloads(ctx context.Context, now time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.ExpireAgentSwitchFailurePayloads(ctx, now)
}

// ResolveAgentSwitchFailureReceipts retires obsolete receipts and removes expired receipt metadata.
func (s *Store) ResolveAgentSwitchFailureReceipts(ctx context.Context, input ports.AgentSwitchFailureReceiptResolution) (int64, error) {
	if input.ResolvedAt.IsZero() {
		return 0, errors.New("resolve agent switch failure receipts: timestamp is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	n, err := q.ResolveAgentSwitchFailureReceipts(ctx, gen.ResolveAgentSwitchFailureReceiptsParams{
		RetainUntil:             sql.NullTime{Time: input.ResolvedAt.Add(agentSwitchFailureTTL), Valid: true},
		SwitchID:                sql.NullString{String: string(input.SwitchID), Valid: true},
		DurableStateFingerprint: input.DurableStateFingerprint,
	})
	if err != nil {
		return 0, err
	}
	d, err := q.DeleteExpiredAgentSwitchFailureReceipts(ctx, sql.NullTime{Time: input.ResolvedAt, Valid: true})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n + d, nil
}

// AgentSwitchFailureBacklog summarizes queued, leased, delivered, and discarded failure payloads.
func (s *Store) AgentSwitchFailureBacklog(ctx context.Context, now time.Time) (ports.AgentSwitchFailureBacklog, error) {
	row, err := s.qr.AgentSwitchFailureBacklog(ctx, now)
	if err != nil {
		return ports.AgentSwitchFailureBacklog{}, err
	}
	out := ports.AgentSwitchFailureBacklog{Pending: row.Pending, Leased: row.Leased, Delivered: row.Delivered, Discarded: row.Discarded}
	switch oldest := row.OldestDue.(type) {
	case time.Time:
		out.OldestDue = oldest
	case string:
		parsed, parseErr := time.Parse(time.RFC3339Nano, oldest)
		if parseErr != nil {
			return ports.AgentSwitchFailureBacklog{}, parseErr
		}
		out.OldestDue = parsed
	case nil:
	default:
		return ports.AgentSwitchFailureBacklog{}, fmt.Errorf("unsupported oldest agent switch failure timestamp type %T", row.OldestDue)
	}
	return out, nil
}
