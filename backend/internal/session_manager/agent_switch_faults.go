package sessionmanager

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// agentSwitchFlightRecorder retains only closed, privacy-safe observations for
// one admitted execution. It is deliberately not a breadcrumb trail: raw
// errors, identifiers, paths, provider values, and user content have no field
// in this type.
type agentSwitchFlightRecorder struct {
	failurePoint             domain.AgentSwitchFailurePoint
	lastDurablePhase         domain.AgentSwitchState
	callOutcome              domain.AgentSwitchCallOutcome
	ownership                domain.AgentSwitchOwnership
	compensation             domain.AgentSwitchCompensation
	userImpact               domain.AgentSwitchUserImpact
	sourceStopConfirmed      domain.AgentSwitchTriState
	targetOwnerCommitted     domain.AgentSwitchTriState
	targetOwnershipAmbiguous bool
	gateRetained             domain.AgentSwitchTriState
	execution                domain.AgentSwitchExecution
	mode                     domain.SessionMode
	runtimeBackend           domain.AgentSwitchRuntimeBackend
}

type agentSwitchPanicCause struct {
	failurePoint     domain.AgentSwitchFailurePoint
	executionAttempt string
	frames           []domain.AgentSwitchStackFrame
}

type agentSwitchPanicCauseContextKey struct{}

func withAgentSwitchPanicCause(ctx context.Context, cause agentSwitchPanicCause) context.Context {
	return context.WithValue(ctx, agentSwitchPanicCauseContextKey{}, cause)
}

// agentSwitchAdmissionFailure carries only the closed classification needed at
// the HTTP ownership boundary. It is deliberately not reportable by the saga:
// durable admission has not been proven at these call sites.
type agentSwitchAdmissionFailure struct {
	point domain.AgentSwitchFailurePoint
	err   error
}

func (e *agentSwitchAdmissionFailure) Error() string { return e.err.Error() }
func (e *agentSwitchAdmissionFailure) Unwrap() error { return e.err }

func classifyAgentSwitchAdmissionFailure(point domain.AgentSwitchFailurePoint, err error) error {
	if err == nil {
		return nil
	}
	return &agentSwitchAdmissionFailure{point: point, err: err}
}

func newAgentSwitchFlightRecorder(sw domain.AgentSwitch, mode domain.SessionMode, execution domain.AgentSwitchExecution) agentSwitchFlightRecorder {
	backend := domain.AgentSwitchRuntimeTMUX
	if mode == domain.SessionModeChat {
		backend = domain.AgentSwitchRuntimeChatController
	} else if runtime.GOOS == "windows" {
		backend = domain.AgentSwitchRuntimeConPTY
	}
	recorder := agentSwitchFlightRecorder{
		lastDurablePhase:     sw.State,
		callOutcome:          domain.AgentSwitchCallNoEffectFailure,
		ownership:            domain.AgentSwitchOwnershipSource,
		compensation:         domain.AgentSwitchCompensationNotNeeded,
		userImpact:           domain.AgentSwitchUserImpactSourceAvailable,
		sourceStopConfirmed:  domain.AgentSwitchTriFalse,
		targetOwnerCommitted: domain.AgentSwitchTriFalse,
		gateRetained:         domain.AgentSwitchTriFalse,
		execution:            execution,
		mode:                 mode,
		runtimeBackend:       backend,
	}
	recorder.durable(sw)
	return recorder
}

func (r *agentSwitchFlightRecorder) boundary(point domain.AgentSwitchFailurePoint) {
	r.failurePoint = point
	r.callOutcome = domain.AgentSwitchCallNoEffectFailure
}

func (r *agentSwitchFlightRecorder) durable(sw domain.AgentSwitch) {
	r.lastDurablePhase = sw.State
	if sw.State == domain.AgentSwitchSourceStopped || sw.State == domain.AgentSwitchStartingTarget ||
		sw.State == domain.AgentSwitchTargetReady || sw.State == domain.AgentSwitchDelivering || sw.State == domain.AgentSwitchCompleted {
		r.sourceStopConfirmed = domain.AgentSwitchTriTrue
	}
	if sw.State == domain.AgentSwitchSourceStopped || sw.State == domain.AgentSwitchStartingTarget {
		r.ownership = domain.AgentSwitchOwnershipNone
		r.userImpact = domain.AgentSwitchUserImpactNoLiveOwner
	}
	if sw.State == domain.AgentSwitchTargetReady || sw.State == domain.AgentSwitchDelivering || sw.State == domain.AgentSwitchCompleted {
		r.targetOwnerCommitted = domain.AgentSwitchTriTrue
		r.ownership = domain.AgentSwitchOwnershipTarget
	}
}

func (r *agentSwitchFlightRecorder) retain(ambiguous bool) {
	r.gateRetained = domain.AgentSwitchTriTrue
	r.targetOwnershipAmbiguous = ambiguous
	if ambiguous {
		r.ownership = domain.AgentSwitchOwnershipAmbiguous
		r.userImpact = domain.AgentSwitchUserImpactOwnershipAmbiguous
	} else {
		r.userImpact = domain.AgentSwitchUserImpactGateRetained
	}
}

func (m *Manager) agentSwitchAuthorization() domain.AgentSwitchReportingAuthorization {
	if m.agentSwitchReporting == nil {
		return domain.AgentSwitchReportingAuthorization{}
	}
	return m.agentSwitchReporting.Authorization()
}

func (m *Manager) faultFromRecorder(sw domain.AgentSwitch, code domain.AgentSwitchErrorCode, reportKind domain.AgentSwitchReportKind, recorder agentSwitchFlightRecorder) domain.AgentSwitchFault {
	point := recorder.failurePoint
	entry, ok := domain.AgentSwitchFailureTaxonomy(point)
	callsite := domain.AgentSwitchClassifierInvariant
	if ok {
		callsite = entry.ClassifierCallsite
	} else {
		point = domain.AgentSwitchFailureClassificationUnknown
	}
	startMode := sw.TargetStartMode
	if startMode == "" {
		startMode = domain.AgentSwitchTargetStartReportedPending
	}
	return domain.AgentSwitchFault{
		ReportKind: reportKind, FailurePoint: point, ClassifierCallsite: callsite,
		Phase: sw.State, ErrorCode: code, FaultCode: domain.AgentSwitchFaultNotApplicable,
		Execution: recorder.execution, Mode: recorder.mode,
		FromHarness: sw.FromHarness, TargetHarness: sw.TargetHarness,
		TargetStartMode: startMode, RuntimeBackend: recorder.runtimeBackend,
		CallOutcome: recorder.callOutcome, Ownership: recorder.ownership,
		Compensation: recorder.compensation, UserImpact: recorder.userImpact,
		SourceStopConfirmed:  recorder.sourceStopConfirmed,
		TargetOwnerCommitted: recorder.targetOwnerCommitted,
		GateRetained:         recorder.gateRetained,
		OccurredAt:           m.clock(), Frames: captureAgentSwitchFrames(5),
	}
}

// semanticFaultFromRecorder promotes a worker panic into the single fault
// enrolled by a winning terminal/marker mutation. The raw panic value has no
// representation here; only a bounded execution token and sanitized frames
// cross the store boundary.
func (m *Manager) semanticFaultFromRecorder(
	ctx context.Context,
	sw domain.AgentSwitch,
	code domain.AgentSwitchErrorCode,
	reportKind domain.AgentSwitchReportKind,
	recorder agentSwitchFlightRecorder,
) domain.AgentSwitchFault {
	fault := m.faultFromRecorder(sw, code, reportKind, recorder)
	cause, ok := ctx.Value(agentSwitchPanicCauseContextKey{}).(agentSwitchPanicCause)
	if !ok || cause.failurePoint == "" || cause.executionAttempt == "" || len(cause.frames) == 0 {
		return fault
	}
	entry, known := domain.AgentSwitchFailureTaxonomy(cause.failurePoint)
	if !known {
		return fault
	}
	fault.ReportKind = domain.AgentSwitchReportPanic
	fault.FailurePoint = cause.failurePoint
	fault.ClassifierCallsite = entry.ClassifierCallsite
	fault.ErrorCode = domain.AgentSwitchErrorNotApplicable
	fault.FaultCode = domain.AgentSwitchFaultWorkerPanic
	fault.CallOutcome = domain.AgentSwitchCallPanic
	fault.ExecutionAttemptID = cause.executionAttempt
	fault.Frames = append([]domain.AgentSwitchStackFrame(nil), cause.frames...)
	return fault
}

// settleAgentSwitchFault is the only Session Manager adapter from a semantic
// classification to the atomic store contract. Enrollment status is never
// interpreted here: CoreChanged alone decides saga behavior.
func (m *Manager) settleAgentSwitchFault(
	ctx context.Context,
	store ports.AgentSwitchStore,
	next *domain.AgentSwitch,
	expectedState domain.AgentSwitchState,
	expectedTargetGeneration domain.AgentGenerationID,
	fault domain.AgentSwitchFault,
	unacknowledged bool,
) (ports.AgentSwitchMutationResult, error) {
	next.FailurePoint = fault.FailurePoint
	mutation := ports.AgentSwitchMutation{
		Record: *next, ExpectedState: expectedState,
		ExpectedSourceGenerationID: next.SourceGenerationID,
		ExpectedTargetGenerationID: expectedTargetGeneration,
		Fault:                      &fault, Authorization: m.agentSwitchAuthorization(),
	}
	faultStore, ok := store.(ports.AgentSwitchFaultStore)
	if !ok {
		return ports.AgentSwitchMutationResult{}, errors.New("agent switch store does not support typed failure settlement")
	}
	if unacknowledged {
		return faultStore.FailAgentSwitchIfUnacknowledgedWithFault(ctx, mutation)
	}
	return faultStore.ApplyAgentSwitchMutation(ctx, mutation)
}

func (m *Manager) applyAgentSwitchProgress(
	ctx context.Context,
	store ports.AgentSwitchStore,
	next domain.AgentSwitch,
	expectedState domain.AgentSwitchState,
	expectedTargetGeneration domain.AgentGenerationID,
) (ports.AgentSwitchMutationResult, error) {
	if faultStore, ok := store.(ports.AgentSwitchFaultStore); ok {
		return faultStore.ApplyAgentSwitchMutation(ctx, ports.AgentSwitchMutation{
			Record: next, ExpectedState: expectedState,
			ExpectedSourceGenerationID: next.SourceGenerationID,
			ExpectedTargetGenerationID: expectedTargetGeneration,
			Authorization:              m.agentSwitchAuthorization(),
		})
	}
	changed, err := store.UpdateAgentSwitch(ctx, next, expectedState, next.SourceGenerationID, expectedTargetGeneration)
	return ports.AgentSwitchMutationResult{CoreChanged: changed, Enrollment: domain.AgentSwitchEnrollmentDisabled}, err
}

func captureAgentSwitchFrames(skip int) []domain.AgentSwitchStackFrame {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	out := make([]domain.AgentSwitchStackFrame, 0, 12)
	for len(out) < 12 {
		frame, more := frames.Next()
		filename := filepathFromRepository(frame.File)
		pkg, function := safeGoFrameFunction(frame.Function)
		if filename != "" && pkg != "" && function != "" && frame.Line > 0 {
			out = append(out, domain.AgentSwitchStackFrame{Package: pkg, Function: function, Filename: filename, Line: frame.Line})
		}
		if !more {
			break
		}
	}
	return out
}

func filepathFromRepository(filename string) string {
	filename = strings.ReplaceAll(filename, `\`, "/")
	for _, root := range []string{"/backend/", "/frontend/"} {
		if idx := strings.LastIndex(filename, root); idx >= 0 {
			relative := filename[idx+1:]
			if len(relative) <= 512 {
				return relative
			}
			return ""
		}
	}
	return ""
}

func safeGoFrameFunction(full string) (string, string) {
	full = strings.TrimSpace(full)
	lastSlash := strings.LastIndex(full, "/")
	leaf := full
	prefix := ""
	if lastSlash >= 0 {
		prefix, leaf = full[:lastSlash], full[lastSlash+1:]
	}
	firstDot := strings.Index(leaf, ".")
	if firstDot <= 0 || firstDot == len(leaf)-1 {
		return "", ""
	}
	pkgLeaf, function := leaf[:firstDot], leaf[firstDot+1:]
	function = strings.NewReplacer("(", "", ")", "", "*", "", "[", "", "]", "").Replace(function)
	if strings.ContainsAny(function, " /:$") {
		return "", ""
	}
	pkg := pkgLeaf
	if prefix != "" {
		if idx := strings.LastIndex(prefix, "/backend/"); idx >= 0 {
			pkg = strings.TrimPrefix(prefix[idx+len("/backend/"):]+"/"+pkgLeaf, "internal/")
		} else if idx := strings.LastIndex(prefix, "/frontend/"); idx >= 0 {
			pkg = prefix[idx+len("/frontend/"):] + "/" + pkgLeaf
		}
	}
	if len(pkg) > 128 || len(function) > 128 {
		return "", ""
	}
	return pkg, function
}

func sanitizeAgentSwitchPanicStack(stack []byte) []domain.AgentSwitchStackFrame {
	lines := strings.Split(strings.ReplaceAll(string(stack), `\`, "/"), "\n")
	out := make([]domain.AgentSwitchStackFrame, 0, 12)
	for i := 0; i+1 < len(lines) && len(out) < 12; i++ {
		functionLine := strings.TrimSpace(lines[i])
		fileLine := strings.TrimSpace(lines[i+1])
		if functionLine == "" || !strings.Contains(fileLine, ".go:") {
			continue
		}
		if idx := strings.LastIndex(functionLine, "("); idx >= 0 {
			functionLine = functionLine[:idx]
		}
		colon := strings.LastIndex(fileLine, ":")
		if colon < 0 {
			continue
		}
		lineText := fileLine[colon+1:]
		if space := strings.IndexByte(lineText, ' '); space >= 0 {
			lineText = lineText[:space]
		}
		line, err := strconv.Atoi(lineText)
		if err != nil || line <= 0 {
			continue
		}
		filename := filepathFromRepository(fileLine[:colon])
		pkg, function := safeGoFrameFunction(functionLine)
		if filename == "" || pkg == "" || function == "" {
			continue
		}
		out = append(out, domain.AgentSwitchStackFrame{Package: pkg, Function: function, Filename: filename, Line: line})
		i++
	}
	return out
}

func (m *Manager) observeAgentSwitchRecoveryFailure(
	ctx context.Context,
	store ports.AgentSwitchStore,
	before domain.AgentSwitch,
	mode domain.SessionMode,
	execution domain.AgentSwitchExecution,
	recoveryErr error,
) {
	if recoveryErr == nil || errors.Is(recoveryErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	reloadCtx, cancel := agentSwitchDetachedContext(ctx)
	current, found, err := store.GetAgentSwitch(reloadCtx, before.ID)
	cancel()
	if err != nil || !found || current.State != before.State || current.ErrorCode != before.ErrorCode ||
		current.FailurePoint != before.FailurePoint || !current.UpdatedAt.Equal(before.UpdatedAt) {
		return
	}
	m.observeUnchangedAgentSwitchRecoveryFailure(ctx, store, current, mode, execution)
}

func (m *Manager) observeUnchangedAgentSwitchRecoveryFailure(
	ctx context.Context,
	store ports.AgentSwitchStore,
	sw domain.AgentSwitch,
	mode domain.SessionMode,
	execution domain.AgentSwitchExecution,
) {
	if !sw.ErrorCode.RetainedRecoveryMarker() {
		return
	}
	faultStore, ok := store.(ports.AgentSwitchFaultStore)
	if !ok {
		return
	}
	recorder := newAgentSwitchFlightRecorder(sw, mode, execution)
	recorder.failurePoint = domain.AgentSwitchFailureRecoveryExistingMarker
	recorder.callOutcome = domain.AgentSwitchCallNoEffectFailure
	recorder.compensation = domain.AgentSwitchCompensationUncertain
	recorder.retain(sw.RequiresTargetStartRecovery())
	recorder.durable(sw)
	fault := m.faultFromRecorder(sw, sw.ErrorCode, domain.AgentSwitchReportRecoveryAttemptFailed, recorder)
	fault.FaultCode = domain.AgentSwitchFaultRecoveryUnresolved
	enqueueCtx, cancel := agentSwitchDetachedContext(ctx)
	defer cancel()
	result, err := faultStore.EnqueueAgentSwitchOperationalFault(enqueueCtx, ports.AgentSwitchOperationalFault{
		SwitchID: sw.ID, ExpectedState: sw.State, ExpectedErrorCode: sw.ErrorCode,
		ExpectedFailurePoint: sw.FailurePoint, ExpectedUpdatedAt: sw.UpdatedAt,
		Fault: fault, Authorization: m.agentSwitchAuthorization(),
	})
	if err != nil {
		m.logger.Warn("agent switch recovery observability enqueue failed", "state", sw.State, "errorCode", sw.ErrorCode, "error", err)
		return
	}
	if result.Enrollment == domain.AgentSwitchEnrollmentLocalInvariantFailed {
		m.logger.Warn("agent switch recovery observability invariant failed", "state", sw.State, "errorCode", sw.ErrorCode)
	}
}

func (m *Manager) enqueueAgentSwitchPanic(
	ctx context.Context,
	store ports.AgentSwitchStore,
	sw domain.AgentSwitch,
	mode domain.SessionMode,
	execution domain.AgentSwitchExecution,
	attemptID string,
	point domain.AgentSwitchFailurePoint,
	frames []domain.AgentSwitchStackFrame,
) {
	faultStore, ok := store.(ports.AgentSwitchFaultStore)
	if !ok || len(frames) == 0 {
		return
	}
	recorder := newAgentSwitchFlightRecorder(sw, mode, execution)
	recorder.failurePoint = point
	recorder.callOutcome = domain.AgentSwitchCallPanic
	recorder.retain(sw.RequiresRecovery())
	recorder.durable(sw)
	fault := m.faultFromRecorder(sw, domain.AgentSwitchErrorNotApplicable, domain.AgentSwitchReportPanic, recorder)
	fault.FaultCode = domain.AgentSwitchFaultWorkerPanic
	fault.ExecutionAttemptID = attemptID
	fault.Frames = append([]domain.AgentSwitchStackFrame(nil), frames...)
	enqueueCtx, cancel := agentSwitchDetachedContext(ctx)
	defer cancel()
	_, err := faultStore.EnqueueAgentSwitchOperationalFault(enqueueCtx, ports.AgentSwitchOperationalFault{
		SwitchID: sw.ID, ExpectedState: sw.State, ExpectedErrorCode: sw.ErrorCode,
		ExpectedFailurePoint: sw.FailurePoint, ExpectedUpdatedAt: sw.UpdatedAt,
		Fault: fault, Authorization: m.agentSwitchAuthorization(),
	})
	if err != nil {
		m.logger.Warn("agent switch panic observability enqueue failed", "state", sw.State, "errorCode", sw.ErrorCode, "error", err)
	}
}

func (m *Manager) observeTerminalAgentSwitchMaintenanceFailure(
	ctx context.Context,
	store ports.AgentSwitchStore,
	sw domain.AgentSwitch,
	mode domain.SessionMode,
	execution domain.AgentSwitchExecution,
) {
	if !sw.State.Terminal() || strings.TrimSpace(m.daemonRunID) == "" {
		return
	}
	faultStore, ok := store.(ports.AgentSwitchFaultStore)
	if !ok {
		return
	}
	recorder := newAgentSwitchFlightRecorder(sw, mode, execution)
	recorder.failurePoint = domain.AgentSwitchFailureTerminalArtifactCleanup
	recorder.callOutcome = domain.AgentSwitchCallCleanupFailed
	recorder.compensation = domain.AgentSwitchCompensationFailed
	recorder.durable(sw)
	code := sw.ErrorCode
	if sw.State == domain.AgentSwitchCompleted {
		code = domain.AgentSwitchErrorNotApplicable
	}
	fault := m.faultFromRecorder(sw, code, domain.AgentSwitchReportMaintenanceFailure, recorder)
	fault.FaultCode = domain.AgentSwitchFaultTerminalCleanupFailed
	enqueueCtx, cancel := agentSwitchDetachedContext(ctx)
	defer cancel()
	_, err := faultStore.EnqueueAgentSwitchOperationalFault(enqueueCtx, ports.AgentSwitchOperationalFault{
		SwitchID: sw.ID, ExpectedState: sw.State, ExpectedErrorCode: sw.ErrorCode,
		ExpectedFailurePoint: sw.FailurePoint, ExpectedUpdatedAt: sw.UpdatedAt,
		DaemonRunID: m.daemonRunID, Fault: fault, Authorization: m.agentSwitchAuthorization(),
	})
	if err != nil {
		m.logger.Warn("agent switch maintenance observability enqueue failed", "state", sw.State, "errorCode", sw.ErrorCode, "error", err)
	}
}

func agentSwitchDetachedContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}
